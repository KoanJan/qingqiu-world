// Package tools provides task-loop domain tool implementations.
// Reusable path-based tool cores are in service/tools; this package wraps them
// with the task-specific Tool interface, Schema generation, and ID-to-path translation.
package tools

import servicetools "qingqiu-world-server/internal/service/tools"

// CycleDetector is an alias of service/tools.CycleDetector to keep the Tool
// interface working with promoted CycleDetect methods.
type CycleDetector = servicetools.CycleDetector

// CycleStatus is an alias of service/tools.CycleStatus.
type CycleStatus = servicetools.CycleStatus

// NoCycleDetected is an alias of service/tools.NoCycleDetected.
var NoCycleDetected = servicetools.NoCycleDetected
