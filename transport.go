package pidalio

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"

	jsonpatch "github.com/evanphx/json-patch"
	jsonpatchv2 "gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/k-cloud-labs/pkg/utils"
	"github.com/k-cloud-labs/pkg/utils/interrupter"
	"github.com/k-cloud-labs/pkg/utils/overridemanager"
)

type policyTransport struct {
	delegate http.RoundTripper

	overrideManager   overridemanager.OverrideManager
	policyInterrupter interrupter.PolicyInterrupter
}

var _ http.RoundTripper = &policyTransport{}

// RegisterPolicyTransport init transport and register to wrapper.
func RegisterPolicyTransport(config *rest.Config, stopCh chan struct{}) {
	var (
		p = &policyTransport{}
		s = &setupManager{}
	)
	config.Wrap(p.Wrap)

	if err := s.setupAll(config, stopCh); err != nil {
		klog.Fatalf("setup transport failed with error=%v", err)
	}

	p.overrideManager = s.overrideManager
	p.policyInterrupter = s.policyInterrupterManager

	if err := s.waitForCacheSync(); err != nil {
		klog.Fatalf("sync cache failed with error=%v", err)
	} // wait sync policies
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

	var operation admissionv1.Operation
	if req.Method == http.MethodPost {
		operation = admissionv1.Create
	} else {
		operation = admissionv1.Update
	}

	patches, err := tr.policyInterrupter.OnMutating(unstructuredObj, nil, operation)
	if err != nil {
		return nil, err
	}

	if len(patches) > 0 {
		if err = applyJSONPatch(unstructuredObj, patches); err != nil {
			return nil, err
		}
	} else {
		if err = ApplyOverridePolicy(tr.overrideManager, unstructuredObj, operation); err != nil {
			return nil, err
		}
	}

	newBody, err := unstructuredObj.MarshalJSON()
	if err != nil {
		return nil, err
	}

	req.Body = ioutil.NopCloser(bytes.NewBuffer(newBody))
	req.ContentLength = int64(len(newBody))

	return tr.delegate.RoundTrip(req)
}

func ApplyOverridePolicy(manager overridemanager.OverrideManager, unstructuredObj *unstructured.Unstructured, operation admissionv1.Operation) error {
	cops, ops, err := manager.ApplyOverridePolicies(unstructuredObj, nil, operation)
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
			annotations[utils.AppliedClusterOverrides] = string(appliedBytes)
		}
	}

	if ops != nil {
		appliedBytes, err := ops.MarshalJSON()
		if err != nil {
			return nil, err
		}
		if appliedBytes != nil {
			annotations[utils.AppliedOverrides] = string(appliedBytes)
		}
	}

	return annotations, nil
}

// applyJSONPatch applies the override on to the given unstructured object.
func applyJSONPatch(obj *unstructured.Unstructured, overrides []jsonpatchv2.JsonPatchOperation) error {
	jsonPatchBytes, err := json.Marshal(overrides)
	if err != nil {
		return err
	}

	patch, err := jsonpatch.DecodePatch(jsonPatchBytes)
	if err != nil {
		return err
	}

	objectJSONBytes, err := obj.MarshalJSON()
	if err != nil {
		return err
	}

	patchedObjectJSONBytes, err := patch.Apply(objectJSONBytes)
	if err != nil {
		return err
	}

	err = obj.UnmarshalJSON(patchedObjectJSONBytes)
	return err
}
