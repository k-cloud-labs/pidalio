package pidalio

import (
	"bytes"
	"io/ioutil"
	"net/http"

	"github.com/k-cloud-labs/pkg/client/clientset/versioned"
	"github.com/k-cloud-labs/pkg/client/informers/externalversions"
	"github.com/k-cloud-labs/pkg/util"
	"github.com/k-cloud-labs/pkg/util/overridemanager"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

type policyTransport struct {
	delegate        http.RoundTripper
	overrideManager overridemanager.OverrideManager
}

var _ http.RoundTripper = &policyTransport{}

func NewPolicyTransport(config *rest.Config, stopCh chan struct{}) *policyTransport {
	client := versioned.NewForConfigOrDie(config)

	factory := externalversions.NewSharedInformerFactory(client, 0)
	opInformer := factory.Policy().V1alpha1().OverridePolicies()
	copInformer := factory.Policy().V1alpha1().ClusterOverridePolicies()
	opInformer.Informer()
	copInformer.Informer()

	factory.Start(stopCh)
	factory.WaitForCacheSync(stopCh)

	return &policyTransport{
		overrideManager: overridemanager.NewOverrideManager(copInformer.Lister(), opInformer.Lister()),
	}
}

func (tr *policyTransport) Wrap(delegate http.RoundTripper) http.RoundTripper {
	tr.delegate = delegate
	return tr
}

func (tr *policyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost && req.Method != http.MethodPatch && req.Method != http.MethodPut {
		return tr.delegate.RoundTrip(req)
	}

	bodyBytes, err := ioutil.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	unstructuredObj, err := bytesToUnstructured(bodyBytes)
	if err != nil {
		return nil, err
	}

	var operation string
	if req.Method == http.MethodPost {
		operation = util.Create
	} else {
		operation = util.Update
	}

	if err := ApplyOverridePolicy(tr.overrideManager, unstructuredObj, operation); err != nil {
		return nil, err
	}

	newBody, err := unstructuredObj.MarshalJSON()
	if err != nil {
		return nil, err
	}

	req.Body = ioutil.NopCloser(bytes.NewBuffer(newBody))
	req.ContentLength = int64(len(newBody))

	return tr.delegate.RoundTrip(req)
}

func ApplyOverridePolicy(manager overridemanager.OverrideManager, unstructuredObj *unstructured.Unstructured, operation string) error {
	cops, ops, err := manager.ApplyOverridePolicies(unstructuredObj, operation)
	if err != nil {
		klog.ErrorS(err, "Failed to apply overrides.", "resource", klog.KObj(unstructuredObj))
		return err
	}

	annotations, err := recordAppliedOverrides(cops, ops, unstructuredObj.GetAnnotations())
	if err != nil {
		klog.ErrorS(err, "failed to record appliedOverrides.", klog.KObj(unstructuredObj))
		return err
	}

	unstructuredObj.SetAnnotations(annotations)

	return nil
}

func bytesToUnstructured(bytes []byte) (*unstructured.Unstructured, error) {
	unstructuredObj := unstructured.Unstructured{}
	err := unstructuredObj.UnmarshalJSON(bytes)
	if err != nil {
		return nil, err
	}

	return &unstructuredObj, nil
}

func recordAppliedOverrides(cops *overridemanager.AppliedOverrides, ops *overridemanager.AppliedOverrides,
	annotations map[string]string) (map[string]string, error) {
	if annotations == nil {
		annotations = make(map[string]string)
	}

	if cops != nil {
		appliedBytes, err := cops.MarshalJSON()
		if err != nil {
			return nil, err
		}
		if appliedBytes != nil {
			annotations[util.AppliedClusterOverrides] = string(appliedBytes)
		}
	}

	if ops != nil {
		appliedBytes, err := ops.MarshalJSON()
		if err != nil {
			return nil, err
		}
		if appliedBytes != nil {
			annotations[util.AppliedOverrides] = string(appliedBytes)
		}
	}

	return annotations, nil
}
