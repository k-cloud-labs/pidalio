package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/k-cloud-labs/pidalio"
)

func main() {
	flag.Parse()

	config := ctrl.GetConfigOrDie()

	// the black magic code
	config.Wrap(pidalio.NewPolicyTransport(config, make(chan struct{})).Wrap)

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

	fmt.Println(pod)
}
