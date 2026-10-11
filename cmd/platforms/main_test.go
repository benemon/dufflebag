package main

import (
	"reflect"
	"testing"
)

func TestUnionIsEveryProductsNewestStableBuilds(t *testing.T) {
	products := []Product{
		{Product: "packer-plugin-amazon", Version: "1.8.3", Platforms: []string{"linux_amd64", "darwin_arm64"}},
		{Product: "packer-plugin-docker", Version: "1.1.4", Platforms: []string{"linux_amd64", "linux_ppc64le"}},
	}
	if got := Union(products); !reflect.DeepEqual(got, []string{"darwin_arm64", "linux_amd64", "linux_ppc64le"}) {
		t.Fatalf("union = %v", got)
	}
}

func TestNewestStableSkipsPrereleasesAndComparesNumerically(t *testing.T) {
	versions := []string{"1.8.3", "1.9.0-beta.1", "1.10.0", "1.8.10", "1.9.0"}
	if got := newestStable(versions); got != "1.10.0" {
		t.Fatalf("newest stable = %q", got)
	}
	if got := newestStable([]string{"2.0.0-rc1"}); got != "" {
		t.Fatalf("a product with only prereleases = %q, want none", got)
	}
}
