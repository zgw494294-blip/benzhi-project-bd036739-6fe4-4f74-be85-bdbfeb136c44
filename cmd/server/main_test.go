package main

import "testing"

func TestParseConfigAddressPolicy(t *testing.T) {
	configuration, err := parseConfig(nil, "")
	if err != nil || configuration.address != defaultAddress {
		t.Fatalf("default config = %#v, %v", configuration, err)
	}
	configuration, err = parseConfig(nil, "19123")
	if err != nil || configuration.address != "127.0.0.1:19123" {
		t.Fatalf("PORT config = %#v, %v", configuration, err)
	}
	for _, address := range []string{"0.0.0.0:19087", ":19087", "[::]:19087"} {
		if _, err := parseConfig([]string{"-addr=" + address}, ""); err == nil {
			t.Errorf("unsafe address %q accepted", address)
		}
	}
}
