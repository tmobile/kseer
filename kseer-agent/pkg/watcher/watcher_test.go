package watcher

import "testing"

func TestHashSecretData_RedactsDataAndStringData(t *testing.T) {
	obj := map[string]interface{}{
		"kind": "Secret",
		"metadata": map[string]interface{}{
			"name": "my-secret",
		},
		"data": map[string]interface{}{
			"username": "dXNlcm5hbWU=",
			"password": "cGFzc3dvcmQ=",
		},
		"stringData": map[string]interface{}{
			"token": "super-secret-token",
		},
	}

	result := hashSecretData(obj)

	data := result["data"].(map[string]interface{})
	for k, v := range data {
		if v != "REDACTED" {
			t.Errorf("data[%q] = %q, want REDACTED", k, v)
		}
	}
	sd := result["stringData"].(map[string]interface{})
	for k, v := range sd {
		if v != "REDACTED" {
			t.Errorf("stringData[%q] = %q, want REDACTED", k, v)
		}
	}
}

func TestHashSecretData_StripsLastAppliedAnnotation(t *testing.T) {
	lacValue := `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"my-secret"},"data":{"password":"cGFzc3dvcmQ="}}`
	obj := map[string]interface{}{
		"kind": "Secret",
		"metadata": map[string]interface{}{
			"name": "my-secret",
			"annotations": map[string]interface{}{
				"kubectl.kubernetes.io/last-applied-configuration": lacValue,
				"other-annotation": "keep-me",
			},
		},
		"data": map[string]interface{}{
			"password": "cGFzc3dvcmQ=",
		},
	}

	result := hashSecretData(obj)

	meta := result["metadata"].(map[string]interface{})
	annots := meta["annotations"].(map[string]interface{})

	if _, exists := annots["kubectl.kubernetes.io/last-applied-configuration"]; exists {
		t.Error("last-applied-configuration annotation should have been stripped but is still present")
	}
	if annots["other-annotation"] != "keep-me" {
		t.Errorf("other-annotation should be preserved, got %v", annots["other-annotation"])
	}
}

func TestHashSecretData_NoAnnotations_NoError(t *testing.T) {
	// Secret with no annotations should not panic
	obj := map[string]interface{}{
		"kind": "Secret",
		"metadata": map[string]interface{}{
			"name": "bare-secret",
		},
		"data": map[string]interface{}{
			"key": "dmFsdWU=",
		},
	}
	result := hashSecretData(obj)
	data := result["data"].(map[string]interface{})
	if data["key"] != "REDACTED" {
		t.Errorf("expected REDACTED, got %v", data["key"])
	}
}

func TestHashSecretData_NonSecret_Passthrough(t *testing.T) {
	// Non-Secret objects must not be modified at all
	obj := map[string]interface{}{
		"kind": "ConfigMap",
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"kubectl.kubernetes.io/last-applied-configuration": "should-stay",
			},
		},
		"data": map[string]interface{}{
			"key": "plaintext",
		},
	}

	result := hashSecretData(obj)

	// Must be the same object (not a copy, and unmodified)
	meta := result["metadata"].(map[string]interface{})
	annots := meta["annotations"].(map[string]interface{})
	if annots["kubectl.kubernetes.io/last-applied-configuration"] != "should-stay" {
		t.Error("non-Secret annotation should not be touched")
	}
	if result["data"].(map[string]interface{})["key"] != "plaintext" {
		t.Error("non-Secret data should not be redacted")
	}
}

func TestHashSecretData_DoesNotMutateOriginal(t *testing.T) {
	lacValue := "sensitive"
	obj := map[string]interface{}{
		"kind": "Secret",
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"kubectl.kubernetes.io/last-applied-configuration": lacValue,
			},
		},
		"data": map[string]interface{}{
			"secret": "value",
		},
	}

	_ = hashSecretData(obj)

	// Original must be untouched
	meta := obj["metadata"].(map[string]interface{})
	annots := meta["annotations"].(map[string]interface{})
	if annots["kubectl.kubernetes.io/last-applied-configuration"] != lacValue {
		t.Error("hashSecretData mutated the original object")
	}
	if obj["data"].(map[string]interface{})["secret"] != "value" {
		t.Error("hashSecretData mutated original data")
	}
}
