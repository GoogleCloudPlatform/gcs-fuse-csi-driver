// Code generated by protoc-gen-validate. DO NOT EDIT.
// source: xds/type/matcher/v3/string.proto

package v3

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/anypb"
)

// ensure the imports are used
var (
	_ = bytes.MinRead
	_ = errors.New("")
	_ = fmt.Print
	_ = utf8.UTFMax
	_ = (*regexp.Regexp)(nil)
	_ = (*strings.Reader)(nil)
	_ = net.IPv4len
	_ = time.Duration(0)
	_ = (*url.URL)(nil)
	_ = (*mail.Address)(nil)
	_ = anypb.Any{}
	_ = sort.Sort
)

// Validate checks the field values on StringMatcher with the rules defined in
// the proto definition for this message. If any rules are violated, the first
// error encountered is returned, or nil if there are no violations.
func (m *StringMatcher) Validate() error {
	return m.validate(false)
}

// ValidateAll checks the field values on StringMatcher with the rules defined
// in the proto definition for this message. If any rules are violated, the
// result is a list of violation errors wrapped in StringMatcherMultiError, or
// nil if none found.
func (m *StringMatcher) ValidateAll() error {
	return m.validate(true)
}

func (m *StringMatcher) validate(all bool) error {
	if m == nil {
		return nil
	}

	var errors []error

	// no validation rules for IgnoreCase

	oneofMatchPatternPresent := false
	switch v := m.MatchPattern.(type) {
	case *StringMatcher_Exact:
		if v == nil {
			err := StringMatcherValidationError{
				field:  "MatchPattern",
				reason: "oneof value cannot be a typed-nil",
			}
			if !all {
				return err
			}
			errors = append(errors, err)
		}
		oneofMatchPatternPresent = true
		// no validation rules for Exact
	case *StringMatcher_Prefix:
		if v == nil {
			err := StringMatcherValidationError{
				field:  "MatchPattern",
				reason: "oneof value cannot be a typed-nil",
			}
			if !all {
				return err
			}
			errors = append(errors, err)
		}
		oneofMatchPatternPresent = true

		if utf8.RuneCountInString(m.GetPrefix()) < 1 {
			err := StringMatcherValidationError{
				field:  "Prefix",
				reason: "value length must be at least 1 runes",
			}
			if !all {
				return err
			}
			errors = append(errors, err)
		}

	case *StringMatcher_Suffix:
		if v == nil {
			err := StringMatcherValidationError{
				field:  "MatchPattern",
				reason: "oneof value cannot be a typed-nil",
			}
			if !all {
				return err
			}
			errors = append(errors, err)
		}
		oneofMatchPatternPresent = true

		if utf8.RuneCountInString(m.GetSuffix()) < 1 {
			err := StringMatcherValidationError{
				field:  "Suffix",
				reason: "value length must be at least 1 runes",
			}
			if !all {
				return err
			}
			errors = append(errors, err)
		}

	case *StringMatcher_SafeRegex:
		if v == nil {
			err := StringMatcherValidationError{
				field:  "MatchPattern",
				reason: "oneof value cannot be a typed-nil",
			}
			if !all {
				return err
			}
			errors = append(errors, err)
		}
		oneofMatchPatternPresent = true

		if m.GetSafeRegex() == nil {
			err := StringMatcherValidationError{
				field:  "SafeRegex",
				reason: "value is required",
			}
			if !all {
				return err
			}
			errors = append(errors, err)
		}

		if all {
			switch v := interface{}(m.GetSafeRegex()).(type) {
			case interface{ ValidateAll() error }:
				if err := v.ValidateAll(); err != nil {
					errors = append(errors, StringMatcherValidationError{
						field:  "SafeRegex",
						reason: "embedded message failed validation",
						cause:  err,
					})
				}
			case interface{ Validate() error }:
				if err := v.Validate(); err != nil {
					errors = append(errors, StringMatcherValidationError{
						field:  "SafeRegex",
						reason: "embedded message failed validation",
						cause:  err,
					})
				}
			}
		} else if v, ok := interface{}(m.GetSafeRegex()).(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				return StringMatcherValidationError{
					field:  "SafeRegex",
					reason: "embedded message failed validation",
					cause:  err,
				}
			}
		}

	case *StringMatcher_Contains:
		if v == nil {
			err := StringMatcherValidationError{
				field:  "MatchPattern",
				reason: "oneof value cannot be a typed-nil",
			}
			if !all {
				return err
			}
			errors = append(errors, err)
		}
		oneofMatchPatternPresent = true

		if utf8.RuneCountInString(m.GetContains()) < 1 {
			err := StringMatcherValidationError{
				field:  "Contains",
				reason: "value length must be at least 1 runes",
			}
			if !all {
				return err
			}
			errors = append(errors, err)
		}

	case *StringMatcher_Custom:
		if v == nil {
			err := StringMatcherValidationError{
				field:  "MatchPattern",
				reason: "oneof value cannot be a typed-nil",
			}
			if !all {
				return err
			}
			errors = append(errors, err)
		}
		oneofMatchPatternPresent = true

		if all {
			switch v := interface{}(m.GetCustom()).(type) {
			case interface{ ValidateAll() error }:
				if err := v.ValidateAll(); err != nil {
					errors = append(errors, StringMatcherValidationError{
						field:  "Custom",
						reason: "embedded message failed validation",
						cause:  err,
					})
				}
			case interface{ Validate() error }:
				if err := v.Validate(); err != nil {
					errors = append(errors, StringMatcherValidationError{
						field:  "Custom",
						reason: "embedded message failed validation",
						cause:  err,
					})
				}
			}
		} else if v, ok := interface{}(m.GetCustom()).(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				return StringMatcherValidationError{
					field:  "Custom",
					reason: "embedded message failed validation",
					cause:  err,
				}
			}
		}

	default:
		_ = v // ensures v is used
	}
	if !oneofMatchPatternPresent {
		err := StringMatcherValidationError{
			field:  "MatchPattern",
			reason: "value is required",
		}
		if !all {
			return err
		}
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		return StringMatcherMultiError(errors)
	}

	return nil
}

// StringMatcherMultiError is an error wrapping multiple validation errors
// returned by StringMatcher.ValidateAll() if the designated constraints
// aren't met.
type StringMatcherMultiError []error

// Error returns a concatenation of all the error messages it wraps.
func (m StringMatcherMultiError) Error() string {
	var msgs []string
	for _, err := range m {
		msgs = append(msgs, err.Error())
	}
	return strings.Join(msgs, "; ")
}

// AllErrors returns a list of validation violation errors.
func (m StringMatcherMultiError) AllErrors() []error { return m }

// StringMatcherValidationError is the validation error returned by
// StringMatcher.Validate if the designated constraints aren't met.
type StringMatcherValidationError struct {
	field  string
	reason string
	cause  error
	key    bool
}

// Field function returns field value.
func (e StringMatcherValidationError) Field() string { return e.field }

// Reason function returns reason value.
func (e StringMatcherValidationError) Reason() string { return e.reason }

// Cause function returns cause value.
func (e StringMatcherValidationError) Cause() error { return e.cause }

// Key function returns key value.
func (e StringMatcherValidationError) Key() bool { return e.key }

// ErrorName returns error name.
func (e StringMatcherValidationError) ErrorName() string { return "StringMatcherValidationError" }

// Error satisfies the builtin error interface
func (e StringMatcherValidationError) Error() string {
	cause := ""
	if e.cause != nil {
		cause = fmt.Sprintf(" | caused by: %v", e.cause)
	}

	key := ""
	if e.key {
		key = "key for "
	}

	return fmt.Sprintf(
		"invalid %sStringMatcher.%s: %s%s",
		key,
		e.field,
		e.reason,
		cause)
}

var _ error = StringMatcherValidationError{}

var _ interface {
	Field() string
	Reason() string
	Key() bool
	Cause() error
	ErrorName() string
} = StringMatcherValidationError{}

// Validate checks the field values on ListStringMatcher with the rules defined
// in the proto definition for this message. If any rules are violated, the
// first error encountered is returned, or nil if there are no violations.
func (m *ListStringMatcher) Validate() error {
	return m.validate(false)
}

// ValidateAll checks the field values on ListStringMatcher with the rules
// defined in the proto definition for this message. If any rules are
// violated, the result is a list of violation errors wrapped in
// ListStringMatcherMultiError, or nil if none found.
func (m *ListStringMatcher) ValidateAll() error {
	return m.validate(true)
}

func (m *ListStringMatcher) validate(all bool) error {
	if m == nil {
		return nil
	}

	var errors []error

	if len(m.GetPatterns()) < 1 {
		err := ListStringMatcherValidationError{
			field:  "Patterns",
			reason: "value must contain at least 1 item(s)",
		}
		if !all {
			return err
		}
		errors = append(errors, err)
	}

	for idx, item := range m.GetPatterns() {
		_, _ = idx, item

		if all {
			switch v := interface{}(item).(type) {
			case interface{ ValidateAll() error }:
				if err := v.ValidateAll(); err != nil {
					errors = append(errors, ListStringMatcherValidationError{
						field:  fmt.Sprintf("Patterns[%v]", idx),
						reason: "embedded message failed validation",
						cause:  err,
					})
				}
			case interface{ Validate() error }:
				if err := v.Validate(); err != nil {
					errors = append(errors, ListStringMatcherValidationError{
						field:  fmt.Sprintf("Patterns[%v]", idx),
						reason: "embedded message failed validation",
						cause:  err,
					})
				}
			}
		} else if v, ok := interface{}(item).(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				return ListStringMatcherValidationError{
					field:  fmt.Sprintf("Patterns[%v]", idx),
					reason: "embedded message failed validation",
					cause:  err,
				}
			}
		}

	}

	if len(errors) > 0 {
		return ListStringMatcherMultiError(errors)
	}

	return nil
}

// ListStringMatcherMultiError is an error wrapping multiple validation errors
// returned by ListStringMatcher.ValidateAll() if the designated constraints
// aren't met.
type ListStringMatcherMultiError []error

// Error returns a concatenation of all the error messages it wraps.
func (m ListStringMatcherMultiError) Error() string {
	var msgs []string
	for _, err := range m {
		msgs = append(msgs, err.Error())
	}
	return strings.Join(msgs, "; ")
}

// AllErrors returns a list of validation violation errors.
func (m ListStringMatcherMultiError) AllErrors() []error { return m }

// ListStringMatcherValidationError is the validation error returned by
// ListStringMatcher.Validate if the designated constraints aren't met.
type ListStringMatcherValidationError struct {
	field  string
	reason string
	cause  error
	key    bool
}

// Field function returns field value.
func (e ListStringMatcherValidationError) Field() string { return e.field }

// Reason function returns reason value.
func (e ListStringMatcherValidationError) Reason() string { return e.reason }

// Cause function returns cause value.
func (e ListStringMatcherValidationError) Cause() error { return e.cause }

// Key function returns key value.
func (e ListStringMatcherValidationError) Key() bool { return e.key }

// ErrorName returns error name.
func (e ListStringMatcherValidationError) ErrorName() string {
	return "ListStringMatcherValidationError"
}

// Error satisfies the builtin error interface
func (e ListStringMatcherValidationError) Error() string {
	cause := ""
	if e.cause != nil {
		cause = fmt.Sprintf(" | caused by: %v", e.cause)
	}

	key := ""
	if e.key {
		key = "key for "
	}

	return fmt.Sprintf(
		"invalid %sListStringMatcher.%s: %s%s",
		key,
		e.field,
		e.reason,
		cause)
}

var _ error = ListStringMatcherValidationError{}

var _ interface {
	Field() string
	Reason() string
	Key() bool
	Cause() error
	ErrorName() string
} = ListStringMatcherValidationError{}
