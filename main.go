package main

import (
	`log`

	`github.com/pkg/errors`
	corev1 "k8s.io/api/core/v1"
	v1 `k8s.io/apimachinery/pkg/apis/meta/v1`
	"k8s.io/helm/cmd/helm/installer"

	`k8s.io/client-go/kubernetes`
	`k8s.io/client-go/tools/clientcmd`
	"sigs.k8s.io/kind/pkg/cluster"
)

func main() {
	ctxKind, err := startKind()
	if err != nil {
		log.Fatal(errors.Wrap(err, "while starting Kind"))
	}
	// defer deleteKind(ctxKind)

	config, err := clientcmd.BuildConfigFromFlags("", ctxKind.KubeConfigPath())
	if err != nil {
		panic(err.Error())
	}
	ns := "kube-system"
	tiller := "tiller"

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	_, err = clientset.CoreV1().ServiceAccounts(ns).Create(&corev1.ServiceAccount{
		ObjectMeta: v1.ObjectMeta{Name: tiller, Namespace: ns},
	})

	if err != nil {
		log.Fatal(errors.Wrap(err, "while creating service acc"))
	}

	// _, err = rbacv1.New(clientset.RESTClient()).ClusterRoleBindings().Create(&typeRbacv1.ClusterRoleBinding{
	// 	ObjectMeta: v1.ObjectMeta{
	// 		Name: tiller,
	// 	},
	// 	Subjects: []typeRbacv1.Subject{{Name: tiller, Namespace: ns}},
	// })
	// if err != nil {
	// 	log.Fatal(errors.Wrap(err, "while creating clusterrolebinding"))
	// }

	// data := clientcmd.GetConfigFromFileOrDie(ctxKind.KubeConfigPath())

	// conf := clientcmd.NewDefaultClientConfig(*data, &clientcmd.ConfigOverrides{})
	err = installer.Install(clientset, &installer.Options{
		ServiceAccount:               "tiller",
		MaxHistory:                   200,
		Namespace:                    ns,
		AutoMountServiceAccountToken: true, // ?
	})
	if err != nil {
		log.Fatal(errors.Wrap(err, "during installation of helm"))
	}

	// for i := 0; i < 10; i++ {
	// 	pods, err := clientset.CoreV1().Pods("").List(v1.ListOptions{})
	// 	if err != nil {
	// 		panic(err.Error())
	// 	}
	// 	fmt.Printf("Pods %v", pods.Items)
	//
	// 	// Examples for error handling:
	// 	// - Use helper functions like e.g. errors.IsNotFound()
	// 	// - And/or cast to StatusError and use its properties like e.g. ErrStatus.Message
	// 	namespace := "kube-system"
	//
	// 	podsKube, err := clientset.CoreV1().Pods(namespace).List(v1.ListOptions{})
	// 	if err == nil {
	// 		fmt.Printf("Found pod %v in namespace %s\n", podsKube, namespace)
	// 	}
	//
	// 	time.Sleep(5 * time.Second)
	// }

	// fmt.Println(config.String())
	// helmClient := helm.NewClient()
	// helmClient.
	// // err := helmClient.PingTiller()
	// resp, err := helmClient.InstallRelease("charts/rafter-controller-manager", "kyma-system")
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// log.Println(resp)

}

func startKind() (*cluster.Context, error) {
	ctx := cluster.NewContext("aerfio")
	err := ctx.Create()
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
