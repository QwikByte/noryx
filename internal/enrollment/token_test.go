package enrollment

import (
	"reflect"
	"testing"
)

func TestTokenRoundTrip(t *testing.T) {
	want := Token{Master: "panel.example.com:9443", NodeID: "n1", Secret: "s3cret", CA: []byte{0x30, 0x82, 0x01}}
	got, err := ParseToken(" " + want.String() + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseTokenRejectsGarbage(t *testing.T) {
	valid := Token{Master: "x", NodeID: "n1", Secret: "s", CA: []byte{1}}.String()
	for _, s := range []string{"", "noryx1_", "noryx1_!!!", Token{Master: "x"}.String(), Token{Master: "x", NodeID: "n1", Secret: "s"}.String(), valid[len("noryx1_"):]} {
		if _, err := ParseToken(s); err == nil {
			t.Errorf("ParseToken(%q) succeeded", s)
		}
	}
}
