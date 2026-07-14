package phpini

import "testing"

func TestValidateAllows(t *testing.T) {
	got, err := Validate(map[string]string{"memory_limit": "512M", "max_input_vars": "5000"})
	if err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	if got["memory_limit"] != "512M" {
		t.Fatalf("unexpected result: %v", got)
	}
}

func TestValidateRejectsUnknownKey(t *testing.T) {
	if _, err := Validate(map[string]string{"disable_functions": ""}); err == nil {
		t.Fatal("non-allowlisted directive must be rejected")
	}
}

func TestValidateRejectsInjection(t *testing.T) {
	bad := []string{"512M\nphp_admin_flag[allow_url_include]=on", "1 2", "on;off"}
	for _, v := range bad {
		if _, err := Validate(map[string]string{"memory_limit": v}); err == nil {
			t.Fatalf("value %q must be rejected", v)
		}
	}
}

func TestValidateDropsEmpty(t *testing.T) {
	got, err := Validate(map[string]string{"memory_limit": ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["memory_limit"]; ok {
		t.Fatal("empty value should be treated as unset")
	}
}
