package savejson

import "testing"

func TestNestedReceiptAndBase64(t *testing.T) {
	got, err := Marshal(struct{ AdmissionInput, Body string }{`{"Mode":2,"Name":"圣"}`, "+w=="})
	const want = `{"AdmissionInput":"{\u0022Mode\u0022:2,\u0022Name\u0022:\u0022\u5723\u0022}","Body":"\u002Bw=="}`
	if err != nil || string(got) != want {
		t.Fatalf("receipt %s, %v", got, err)
	}
}
