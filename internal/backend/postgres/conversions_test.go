package postgres

import (
	"reflect"
	"testing"
	"time"

	durable "github.com/hookship/hook-service/internal/durability/postgres"
	"github.com/hookship/hook-service/internal/runtime"
)

// The durable and runtime models are intentionally distinct types: the
// durability layer must not import runtime, so they cannot be merged. These
// tests guard the bridge instead — if a field is added to a runtime (or durable)
// model but a conversion forgets to populate it, the destination field stays at
// its zero value and the test fails, catching model/conversion drift.

var timeType = reflect.TypeOf(time.Time{})

// fillNonZero recursively sets every settable field of v to a non-zero value.
func fillNonZero(t *testing.T, v reflect.Value) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString("nonzero")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillNonZero(t, v.Index(0))
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			fillNonZero(t, v.Index(i))
		}
	case reflect.Ptr:
		v.Set(reflect.New(v.Type().Elem()))
		fillNonZero(t, v.Elem())
	case reflect.Struct:
		if v.Type() == timeType {
			v.Set(reflect.ValueOf(time.Unix(1_700_000_000, 0).UTC()))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				fillNonZero(t, v.Field(i))
			}
		}
	}
}

// firstZeroField returns the path of the first zero-valued field reachable from
// v, or "" if none. Non-nil pointers and non-empty slices count as non-zero.
func firstZeroField(v reflect.Value, path string) string {
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == timeType {
			if v.Interface().(time.Time).IsZero() {
				return path
			}
			return ""
		}
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if field.PkgPath != "" {
				continue // unexported
			}
			if zero := firstZeroField(v.Field(i), path+"."+field.Name); zero != "" {
				return zero
			}
		}
		return ""
	case reflect.Ptr:
		if v.IsNil() {
			return path
		}
		return ""
	case reflect.Slice:
		if v.Len() == 0 {
			return path
		}
		return ""
	default:
		if v.IsZero() {
			return path
		}
		return ""
	}
}

func assertFullyConverted(t *testing.T, name string, dest any) {
	t.Helper()
	if zero := firstZeroField(reflect.ValueOf(dest), name); zero != "" {
		t.Errorf("%s: field %s was left zero by the conversion (model/conversion drift)", name, zero)
	}
}

func TestToRuntimeReservationParity(t *testing.T) {
	var src durable.Reservation
	fillNonZero(t, reflect.ValueOf(&src).Elem())
	assertFullyConverted(t, "runtime.Reservation", toRuntimeReservation(src))
}

func TestToRuntimeJobParity(t *testing.T) {
	var src durable.Job
	fillNonZero(t, reflect.ValueOf(&src).Elem())
	assertFullyConverted(t, "runtime.Job", toRuntimeJob(src))
}

func TestToRuntimeOutboxItemParity(t *testing.T) {
	var src durable.OutboxItem
	fillNonZero(t, reflect.ValueOf(&src).Elem())
	assertFullyConverted(t, "runtime.OutboxItem", toRuntimeOutboxItem(src))
}

func TestToDurableAttemptParity(t *testing.T) {
	var src runtime.AttemptRecord
	fillNonZero(t, reflect.ValueOf(&src).Elem())
	assertFullyConverted(t, "durable.AttemptRecord", toDurableAttempt(src))
}

func TestToDurableOutboxParity(t *testing.T) {
	var src runtime.OutboxObservation
	fillNonZero(t, reflect.ValueOf(&src).Elem())
	assertFullyConverted(t, "durable.OutboxRecord", toDurableOutbox(src))
}
