package protocol

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"
)

func TestSchemaIsValidJSON(t *testing.T) {
	data, err := Schemas.ReadFile("schema/v" + Version + "/correlation.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatal("missing draft declaration")
	}
}

func TestFixtureValidates(t *testing.T) {
	data, err := os.ReadFile("testdata/correlation.json")
	if err != nil {
		t.Fatal(err)
	}
	var c Correlation
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRef(t *testing.T) {
	for _, ref := range []string{"mail:thread/1", "linear:ENG-123", "mail.archive:x"} {
		if err := ValidateRef(ref); err != nil {
			t.Errorf("ValidateRef(%q) = %v", ref, err)
		}
	}
	for _, ref := range []string{"", "thread-1", "Mail:x", "mail:", "mail:a b", "linear:" + strings.Repeat("x", 256)} {
		if err := ValidateRef(ref); err == nil {
			t.Errorf("ValidateRef(%q) accepted a malformed reference", ref)
		}
	}
}

func TestValidateRequiresCorrelationID(t *testing.T) {
	if err := (Correlation{CausationID: "mail:message/1"}).Validate(); err == nil || !strings.Contains(err.Error(), "correlationid") {
		t.Fatalf("error = %v, want it to name correlationid", err)
	}
}

func TestValidateReportsTheFirstBadFieldInWireOrder(t *testing.T) {
	c := Correlation{CorrelationID: "mail:thread-1", CausationID: "bad", SessionID: "bad"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "causationid") {
		t.Fatalf("error = %v, want causationid", err)
	}
}

func TestAttributesRoundTrip(t *testing.T) {
	c := Correlation{CorrelationID: "mail:thread/1", CausationID: "mail:message/2"}
	attrs := c.Attributes()
	if want := map[string]string{"correlationid": "mail:thread/1", "causationid": "mail:message/2"}; !maps.Equal(attrs, want) {
		t.Fatalf("Attributes() = %v, want %v", attrs, want)
	}
	back, err := FromAttributes(attrs)
	if err != nil || back != c {
		t.Fatalf("FromAttributes = %+v, %v; want %+v", back, err, c)
	}
	if _, err := FromAttributes(map[string]string{}); err == nil {
		t.Fatal("FromAttributes accepted attributes with no correlationid")
	}
}

func ExampleCorrelation_Attributes() {
	c := Correlation{CorrelationID: "mail:thread/42", CausationID: "mail:message/7"}
	data, _ := json.Marshal(c.Attributes())
	fmt.Println(string(data))
	// Output: {"causationid":"mail:message/7","correlationid":"mail:thread/42"}
}
