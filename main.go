package main

import (
	"fmt"
	"log"
	`os`
	`path/filepath`
	`strings`
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	v1rbac "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/helm/cmd/helm/installer"
	"k8s.io/helm/pkg/chartutil"
	`k8s.io/helm/pkg/downloader`
	`k8s.io/helm/pkg/getter`
	"k8s.io/helm/pkg/helm"
	`k8s.io/helm/pkg/helm/environment`
	`k8s.io/helm/pkg/helm/helmpath`
	"k8s.io/helm/pkg/helm/portforwarder"
	`k8s.io/helm/pkg/repo`
	"sigs.k8s.io/kind/pkg/cluster/create"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/kind/pkg/cluster"
)

var (
	kindImage     = "kindest/node:v1.16.2"
	contextName   = "kind-ci"
	TLSCaCertFile = "/Users/i354746/.helm/ca.pem"
	TLSCertFile   = "/Users/i354746/.helm/cert.pem"
	TLSKeyFile    = "/Users/i354746/.helm/key.pem"
	archive       = "/Users/i354746/.helm/cache/archive"
	helmRepo      = "/Users/i354746/.helm/repository"
	home          = "/Users/i354746/.helm"
)

func main() {
	// handle it as well
	// helm repo add rafter-charts https://rafter-charts.storage.googleapis.com
	// helm repo update
	ns := "kube-system"
	tiller := "tiller"
	ctx := cluster.NewContext(contextName)

	if err := ctx.Delete(); err == nil {
		// convenience for local development
		log.Println("Deleted old kind cluster")
	}

	ctxKind, err := startKind()
	if err != nil {
		log.Fatal(errors.Wrap(err, "while starting Kind"))
	}
	// defer deleteKind(ctxKind)

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
		UseCanary:                    false,
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

	cp, err := locateChartPath("", "", "", "rafter-charts/rafter", "", false, defaultKeyring(), "", "", "")
	if err != nil {
		log.Fatal(errors.Wrap(err, "while locating chart path"))
	}
	chartRequested, err := chartutil.Load(cp)
	if err != nil {
		log.Fatal(errors.Wrap(err, "while loading chart"))
	}

	if _, err := chartutil.LoadRequirements(chartRequested); err != nil {
		log.Fatal(errors.Wrap(err, "while loading requirements"))
	}

	resp, err := helmClient.InstallReleaseFromChart(chartRequested, "default", helm.InstallWait(true), helm.ReleaseName("rafter-release"), helm.InstallDescription("data"))
	if err != nil {
		log.Fatal(errors.Wrap(err, "while installing helm chart"))
	}
	fmt.Printf("Resp: %v\n", resp)
}

func defaultKeyring() string {
	return os.ExpandEnv("$HOME/.gnupg/pubring.gpg")
}

func locateChartPath(repoURL, username, password, name, version string, verify bool, keyring,
	certFile, keyFile, caFile string) (string, error) {
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if fi, err := os.Stat(name); err == nil {
		abs, err := filepath.Abs(name)
		if err != nil {
			return abs, err
		}
		if verify {
			if fi.IsDir() {
				return "", errors.New("cannot verify a directory")
			}
			if _, err := downloader.VerifyChart(abs, keyring); err != nil {
				return "", err
			}
		}
		return abs, nil
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, ".") {
		return name, fmt.Errorf("path %q not found", name)
	}

	crepo := filepath.Join(helmRepo, name)
	if _, err := os.Stat(crepo); err == nil {
		return filepath.Abs(crepo)
	}

	dl := downloader.ChartDownloader{
		HelmHome: helmpath.Home(home),
		Out:      os.Stdout,
		Keyring:  keyring,
		Getters:  getter.All(environment.EnvSettings{}),
		Username: username,
		Password: password,
	}
	if verify {
		dl.Verify = downloader.VerifyAlways
	}
	if repoURL != "" {
		chartURL, err := repo.FindChartInAuthRepoURL(repoURL, username, password, name, version,
			certFile, keyFile, caFile, getter.All(environment.EnvSettings{}))
		if err != nil {
			return "", err
		}
		name = chartURL
	}

	if _, err := os.Stat(archive); os.IsNotExist(err) {
		os.MkdirAll(archive, 0744)
	}

	filename, _, err := dl.DownloadTo(name, version, archive)
	if err == nil {
		lname, err := filepath.Abs(filename)
		if err != nil {
			return filename, err
		}

		return lname, nil
	}

	return filename, fmt.Errorf("failed to download %q (hint: running `helm repo update` may help)", name)
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

func setupTillerConnection(config *rest.Config, client kubernetes.Clientset, ns string) (string, error) {
	tillerTunnel, err := portforwarder.New(ns, &client, config)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("127.0.0.1:%d", tillerTunnel.Local), nil
}
