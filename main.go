package main

import (
	`fmt`
	"log"
	`os`
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	v1rbac "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	`k8s.io/client-go/rest`
	"k8s.io/helm/cmd/helm/installer"
	`k8s.io/helm/pkg/chartutil`
	`k8s.io/helm/pkg/downloader`
	`k8s.io/helm/pkg/getter`
	`k8s.io/helm/pkg/helm`
	`k8s.io/helm/pkg/helm/environment`
	`k8s.io/helm/pkg/helm/portforwarder`
	`k8s.io/helm/pkg/renderutil`
	"sigs.k8s.io/kind/pkg/cluster/create"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/kind/pkg/cluster"
)

var (
	kindImage   = "kindest/node:v1.16.2"
	contextName = "kind-ci"
)

func main() {
	ns := "kube-system"
	tiller := "tiller"
	ctx := cluster.NewContext(contextName)

	if err := ctx.Delete(); err == nil {
		log.Println("Deleted old kind cluster")
	}

	ctxKind, err := startKind()
	if err != nil {
		log.Fatal(errors.Wrap(err, "while starting Kind"))
	}
	defer deleteKind(ctxKind)

	config, err := clientcmd.BuildConfigFromFlags("", ctxKind.KubeConfigPath())
	if err != nil {
		log.Fatal(errors.Wrap(err, "while building config from kubeconfig path"))
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(errors.Wrap(err, "while creating Clientset"))
	}
	_, err = clientset.CoreV1().ServiceAccounts(ns).Create(&corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: tiller, Namespace: ns},
	})

	if err != nil {
		log.Fatal(errors.Wrap(err, "while creating Service Account"))
	}

	_, err = clientset.RbacV1().ClusterRoleBindings().Create(&v1rbac.ClusterRoleBinding{RoleRef: v1rbac.RoleRef{
		Kind: "ClusterRole",
		Name: "cluster-admin",
	}, Subjects: []v1rbac.Subject{{
		Kind:      "ServiceAccount",
		Name:      tiller,
		Namespace: ns,
	}}, ObjectMeta: metav1.ObjectMeta{
		Name: tiller,
	}})

	if err != nil {
		log.Fatal(errors.Wrap(err, "while creating ClusterRoleBinding"))
	}

	err = installer.Install(clientset, &installer.Options{
		ServiceAccount:               tiller,
		MaxHistory:                   200,
		Namespace:                    ns,
		AutoMountServiceAccountToken: true,
	})

	if err != nil {
		log.Fatal(errors.Wrap(err, "while installing Tiller"))
	}
	log.Println("Started installing Tiller...")
	watchTillerUntilReady(ns, clientset, 5*60)
	log.Println("Tiller installed and all pods are ready!")

	tillerHost, err := setupTillerConnection(config, *clientset, ns)
	if err != nil {
		log.Fatal(errors.Wrap(err, "while getting Tiller data"))
	}

	options := []helm.Option{helm.Host(tillerHost), helm.ConnectTimeout(int64(300))}
	helmClient := helm.NewClient(options...)

	if err := helmClient.PingTiller(); err != nil {
		log.Fatal(errors.Wrap(err, "while pinging Tiller"))
	}
	log.Println("Tiller successfully pinged")

	chartRequested, err := chartutil.Load("charts/rafter-upload-service")
	if err != nil {
		log.Fatal(errors.Wrap(err, "while loading chart"))
	}

	chartDir := "charts/rafter-asyncapi-service"

	if req, err := chartutil.LoadRequirements(chartRequested); err == nil {
		// If checkDependencies returns an error, we have unfulfilled dependencies.
		// As of Helm 2.4.0, this is treated as a stopping condition:
		// https://github.com/kubernetes/helm/issues/2209
		if err := renderutil.CheckDependencies(chartRequested, req); err != nil {

			man := &downloader.Manager{
				Out:       os.Stdout,
				ChartPath: chartDir,

				SkipUpdate: false,
				Getters:    getter.All(environment.EnvSettings{}),
			}
			if err := man.Update(); err != nil {
				log.Fatal("here")
			}

			// Update all dependencies which are present in /charts.
			chartRequested, err = chartutil.Load(chartDir)
			if err != nil {
				log.Fatal("hereererer")
			}

		}
	} else if err != chartutil.ErrRequirementsNotFound {
		log.Fatal("AWSD")
	}
	
	resp, err := helmClient.InstallReleaseFromChart(chartRequested, ns, helm.InstallWait(true), helm.ReleaseName("rafter-release"), helm.InstallDescription("data"))
	if err != nil {
		log.Fatal(errors.Wrap(err, "while installing helm chart"))
	}
	fmt.Printf("Resp: %v\n", resp)
	// resp, err := helmClient.InstallRelease("charts/rafter-controller-manager", "kyma-system")
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// log.Println(resp)
	time.Sleep(30 * time.Second)
}

func startKind() (*cluster.Context, error) {
	ctx := cluster.NewContext(contextName)
	err := ctx.Create(create.WithNodeImage(kindImage))
	if err != nil {
		return nil, err
	}
	return ctx, nil
}
func deleteKind(ctx *cluster.Context) {
	if err := ctx.Delete(); err != nil {
		log.Fatal(errors.Wrap(err, "while deleting Kind"))
	}
}

func watchTillerUntilReady(namespace string, client kubernetes.Interface, timeout int64) bool {
	deadlinePollingChan := time.NewTimer(time.Duration(timeout) * time.Second).C
	checkTillerPodTicker := time.NewTicker(500 * time.Millisecond)
	doneChan := make(chan bool)

	defer checkTillerPodTicker.Stop()

	go func() {
		for range checkTillerPodTicker.C {
			_, err := portforwarder.GetTillerPodImage(client.CoreV1(), namespace)
			if err == nil {
				doneChan <- true
				break
			}
		}
	}()

	for {
		select {
		case <-deadlinePollingChan:
			return false
		case <-doneChan:
			return true
		}
	}
}

func setupTillerConnection(config *rest.Config, client kubernetes.Clientset, ns string, ) (string, error) {
	tillerTunnel, err := portforwarder.New(ns, &client, config)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("127.0.0.1:%d", tillerTunnel.Local), nil
}
