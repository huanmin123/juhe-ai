package modelcheckprobe

import (
	"reflect"
	"testing"
)

func TestUniqueIdentityModels(t *testing.T) {
	if got := uniqueModels("gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-terra"); !reflect.DeepEqual(got, []string{"gpt-5.6-sol", "gpt-5.6-terra"}) {
		t.Fatalf("models=%v", got)
	}
}
