package store

import (
	"reflect"
	"testing"
)

func TestEveryLimitHasADefaultAndZeroFallsBack(t *testing.T) {
	d := reflect.ValueOf(DefaultLimits())
	for i := 0; i < d.NumField(); i++ {
		if d.Field(i).Int() <= 0 {
			t.Errorf("limit %s has no default", d.Type().Field(i).Name)
		}
	}
	var l Limits
	l.MaxImageMB = 7
	fillLimits(&l)
	if l.MaxImageMB != 7 || l.LogLines != DefaultLimits().LogLines {
		t.Fatalf("fill: %+v", l)
	}
}
