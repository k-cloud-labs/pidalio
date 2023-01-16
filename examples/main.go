package main

import (
	"context"
	"flag"
	"math/rand"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/k-cloud-labs/pidalio"
)

func main() {
	klog.InitFlags(flag.CommandLine)
	flag.Parse()

	config := ctrl.GetConfigOrDie()

	// the black magic code
	pidalio.RegisterPolicyTransport(config, make(chan struct{}))

	client := kubernetes.NewForConfigOrDie(config)

	pod, _ := client.CoreV1().Pods("default").Get(context.Background(), "web-1", metav1.GetOptions{})
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations["random"] = strconv.Itoa(rand.Int())
	pod, err := client.CoreV1().Pods("default").Update(context.Background(), pod, metav1.UpdateOptions{})
	if err != nil {
		panic(err)
	}

	klog.InfoS("update pod success", "pod", pod)
}
