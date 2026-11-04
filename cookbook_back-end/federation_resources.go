package main

import (
	"errors"
	"fmt"
)

const (
	defaultPeerObjectRows    = int64(1_000)
	defaultPeerObjectBytes   = int64(16 << 20)
	defaultGlobalObjectRows  = int64(10_000)
	defaultGlobalObjectBytes = int64(64 << 20)
	defaultPeerEventRows     = int64(10_000)
	defaultPeerEventBytes    = int64(64 << 20)
	defaultGlobalEventRows   = int64(100_000)
	defaultGlobalEventBytes  = int64(256 << 20)
	defaultPeerNonceRows     = int64(1_000)
	defaultPeerNonceBytes    = int64(2 << 20)
	defaultGlobalNonceRows   = int64(10_000)
	defaultGlobalNonceBytes  = int64(16 << 20)
	maxPeerObjectRows        = int64(100_000)
	maxPeerObjectBytes       = int64(512 << 20)
	maxGlobalObjectRows      = int64(1_000_000)
	maxGlobalObjectBytes     = int64(4 << 30)
	maxPeerEventRows         = int64(100_000)
	maxPeerEventBytes        = int64(1 << 30)
	maxGlobalEventRows       = int64(1_000_000)
	maxGlobalEventBytes      = int64(4 << 30)
	maxPeerNonceRows         = int64(100_000)
	maxPeerNonceBytes        = int64(256 << 20)
	maxGlobalNonceRows       = int64(1_000_000)
	maxGlobalNonceBytes      = int64(1 << 30)
)

var errFederationResourceCapacity = errors.New("federation resource capacity reached")
var errFederationResourceInvalid = errors.New("invalid federation resource limits")
var errFederationResourceInUse = errors.New("federation resource limits are below current use")

type federationResourceLimits struct {
	ObjectRows  int64 `json:"object_rows"`
	ObjectBytes int64 `json:"object_bytes"`
	EventRows   int64 `json:"event_rows"`
	EventBytes  int64 `json:"event_bytes"`
	NonceRows   int64 `json:"nonce_rows"`
	NonceBytes  int64 `json:"nonce_bytes"`
}

type federationResourceUsage struct {
	ObjectRows  int64 `json:"object_rows"`
	ObjectBytes int64 `json:"object_bytes"`
	EventRows   int64 `json:"event_rows"`
	EventBytes  int64 `json:"event_bytes"`
	NonceRows   int64 `json:"nonce_rows"`
	NonceBytes  int64 `json:"nonce_bytes"`
}

type federationResourceOrigin struct {
	Origin string                   `json:"origin"`
	Limits federationResourceLimits `json:"limits"`
	Usage  federationResourceUsage  `json:"usage"`
}

type federationResourceSnapshot struct {
	GlobalLimits      federationResourceLimits   `json:"global_limits"`
	DefaultPeerLimits federationResourceLimits   `json:"default_peer_limits"`
	GlobalUsage       federationResourceUsage    `json:"global_usage"`
	Origins           []federationResourceOrigin `json:"origins"`
}

func defaultFederationResourceLimits() federationResourceLimits {
	return federationResourceLimits{ObjectRows: defaultPeerObjectRows, ObjectBytes: defaultPeerObjectBytes, EventRows: defaultPeerEventRows, EventBytes: defaultPeerEventBytes, NonceRows: defaultPeerNonceRows, NonceBytes: defaultPeerNonceBytes}
}

func defaultFederationGlobalResourceLimits() federationResourceLimits {
	return federationResourceLimits{ObjectRows: defaultGlobalObjectRows, ObjectBytes: defaultGlobalObjectBytes, EventRows: defaultGlobalEventRows, EventBytes: defaultGlobalEventBytes, NonceRows: defaultGlobalNonceRows, NonceBytes: defaultGlobalNonceBytes}
}

func validFederationResourceLimits(limits federationResourceLimits, global bool) bool {
	objectRowsMax, objectBytesMax := maxPeerObjectRows, maxPeerObjectBytes
	eventRowsMax, eventBytesMax := maxPeerEventRows, maxPeerEventBytes
	nonceRowsMax, nonceBytesMax := maxPeerNonceRows, maxPeerNonceBytes
	if global {
		objectRowsMax, objectBytesMax = maxGlobalObjectRows, maxGlobalObjectBytes
		eventRowsMax, eventBytesMax = maxGlobalEventRows, maxGlobalEventBytes
		nonceRowsMax, nonceBytesMax = maxGlobalNonceRows, maxGlobalNonceBytes
	}
	return limits.ObjectRows > 0 && limits.ObjectRows <= objectRowsMax &&
		limits.ObjectBytes > 0 && limits.ObjectBytes <= objectBytesMax &&
		limits.EventRows > 0 && limits.EventRows <= eventRowsMax &&
		limits.EventBytes > 0 && limits.EventBytes <= eventBytesMax &&
		limits.NonceRows > 0 && limits.NonceRows <= nonceRowsMax &&
		limits.NonceBytes > 0 && limits.NonceBytes <= nonceBytesMax
}

func validateFederationResourceLimits(limits federationResourceLimits, global bool) error {
	if validFederationResourceLimits(limits, global) {
		return nil
	}
	return fmt.Errorf("%w: values must be positive and within supported maxima", errFederationResourceInvalid)
}
