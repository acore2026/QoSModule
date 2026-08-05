package main

import "testing"

func TestBuildRANDefaults(t *testing.T) {
	defaults, err := buildRANDefaults("123", 0, 1, 0, 20, 21, 100, 101, 0.02, 0.03, 0.4, 0.5)
	if err != nil {
		t.Fatalf("buildRANDefaults() error = %v", err)
	}
	if defaults.Mask != 123 || !defaults.StaticMask || defaults.DLMaxMCS != 20 || defaults.ULMaxMCS != 21 {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	if defaults.DLMaxRB != 100 || defaults.ULMaxRB != 101 {
		t.Fatalf("unexpected RB defaults: %+v", defaults)
	}
	if defaults.DLBLERUpper != 0.02 || defaults.ULBLERUpper != 0.03 {
		t.Fatalf("unexpected BLER defaults: %+v", defaults)
	}
}

func TestBuildRANDefaultsAllowsAutomaticMask(t *testing.T) {
	defaults, err := buildRANDefaults("auto", 0, 1, 0, 28, 28, 273, 273, 0.01, 0.01, 0.5, 0.5)
	if err != nil {
		t.Fatalf("buildRANDefaults() error = %v", err)
	}
	if defaults.Mask != 0 || defaults.StaticMask {
		t.Fatalf("unexpected automatic mask defaults: %+v", defaults)
	}
}

func TestBuildRANDefaultsRejectsInvalidValues(t *testing.T) {
	if _, err := buildRANDefaults("4294967296", 0, 1, 0, 28, 28, 273, 273, 0.01, 0.01, 0.5, 0.5); err == nil {
		t.Fatal("buildRANDefaults() accepted invalid mask")
	}
	if _, err := buildRANDefaults("1", 2, 1, 0, 28, 28, 273, 273, 0.01, 0.01, 0.5, 0.5); err == nil {
		t.Fatal("buildRANDefaults() accepted invalid q-type")
	}
	if _, err := buildRANDefaults("1", 0, 1, 0, 29, 28, 273, 273, 0.01, 0.01, 0.5, 0.5); err == nil {
		t.Fatal("buildRANDefaults() accepted invalid MCS")
	}
	if _, err := buildRANDefaults("1", 0, 1, 0, 28, 28, 274, 273, 0.01, 0.01, 0.5, 0.5); err == nil {
		t.Fatal("buildRANDefaults() accepted invalid RB")
	}
	if _, err := buildRANDefaults("1", 0, 1, 0, 28, 28, 273, 273, 1.01, 0.01, 0.5, 0.5); err == nil {
		t.Fatal("buildRANDefaults() accepted invalid BLER")
	}
}
