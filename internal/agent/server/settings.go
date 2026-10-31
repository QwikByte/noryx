package server

import (
	"cmp"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/agent/runtime"
	"github.com/QwikByte/noryx/internal/agent/storage"
)

// errUnknownType is returned for server types of newer masters.
var errUnknownType = status.Error(codes.FailedPrecondition, "This node's agent doesn't know this server type yet. Update it first.")

// settings are the settings of a server that the requests to create, import and update one
// share, so that withSettings maps them once for all of them.
type settings interface {
	GetName() string
	GetVersion() string
	GetMemoryMb() uint32
	GetPort() uint32
	GetJava() string
	GetRestartPolicy() noryxv1.RestartPolicy
	GetAikarFlags() bool
	GetJvmOptions() []string
	GetCpuMillis() uint32
	GetLoaderVersion() string
	GetStopTimeoutSeconds() uint32
	GetTimeZone() string
	GetExtraPorts() []*noryxv1.ExtraPort
	GetHealthStartMinutes() uint32
	GetCrashLimit() uint32
	GetStableAfterMinutes() uint32
	GetUnhealthyRestartMinutes() uint32
	GetProcessLimit() uint32
	GetInitMemoryMb() uint32
	GetMaxPlayers() uint32
}

// withSettings returns spec with the settings of a request, which checkSettings checks. It
// keeps the server's ID, type, storage location and role in a network.
func withSettings(spec runtime.Spec, s settings) runtime.Spec {
	spec.Name, spec.Version, spec.MemoryMB, spec.Port = s.GetName(), cmp.Or(s.GetVersion(), "LATEST"), s.GetMemoryMb(), s.GetPort()
	spec.Java, spec.RestartPolicy, spec.AikarFlags = s.GetJava(), s.GetRestartPolicy(), s.GetAikarFlags()
	spec.JVMOptions, spec.CPUMillis, spec.LoaderVersion = s.GetJvmOptions(), s.GetCpuMillis(), s.GetLoaderVersion()
	spec.StopTimeout, spec.TimeZone, spec.ExtraPorts = s.GetStopTimeoutSeconds(), s.GetTimeZone(), extraPorts(s.GetExtraPorts())
	spec.HealthStart, spec.CrashLimit, spec.StableAfter = s.GetHealthStartMinutes(), s.GetCrashLimit(), s.GetStableAfterMinutes()
	spec.UnhealthyRestart, spec.ProcessLimit, spec.InitMemoryMB = s.GetUnhealthyRestartMinutes(), s.GetProcessLimit(), s.GetInitMemoryMb()
	spec.MaxPlayers = s.GetMaxPlayers()
	return spec
}

// checkType returns an error unless the agent knows the type of a new server.
func checkType(t noryxv1.ServerType) error {
	_, ok := noryxv1.ServerType_name[int32(t)]
	switch {
	case t == noryxv1.ServerType_SERVER_TYPE_UNSPECIFIED:
		return status.Error(codes.InvalidArgument, "Choose a server type.")
	case !ok:
		return errUnknownType
	}
	return nil
}

// toProto returns a server for the API, with the settings that withSettings takes back.
func toProto(s runtime.Server) *noryxv1.Server {
	return &noryxv1.Server{
		Id: s.ID, Name: s.Name, Type: s.Type, Version: s.Version, MemoryMb: s.MemoryMB, Port: s.Port, State: s.State,
		Storage: cmp.Or(s.Storage, storage.Default), Java: s.Java, RestartPolicy: s.RestartPolicy, AikarFlags: s.AikarFlags,
		JvmOptions: s.JVMOptions, CpuMillis: s.CPUMillis, Crashes: uint32(s.Crashes), ExitCode: int32(s.ExitCode), //nolint:gosec // small numbers
		LoaderVersion: s.LoaderVersion, BedrockPort: s.BedrockPort, RefusedJvmOptions: refusedOptions(s.JVMOptions),
		Overlay: s.Overlay != "", Unhealthy: s.Unhealthy, StopTimeoutSeconds: s.StopTimeout, TimeZone: s.TimeZone,
		ExtraPorts:         protoPorts(s.ExtraPorts),
		HealthStartMinutes: s.HealthStart, CrashLimit: s.CrashLimit, StableAfterMinutes: s.StableAfter, UnhealthyRestartMinutes: s.UnhealthyRestart,
		ProcessLimit: s.ProcessLimit, InitMemoryMb: s.InitMemoryMB, MaxPlayers: s.MaxPlayers,
	}
}
