// Package zenmind contains small compatibility helpers for projecting
// ZenForge runtime events into host-platform adapter shapes.
//
// It intentionally does not import private platform packages. Host code should
// translate these neutral adapter events into concrete server, WebSocket, or
// persistence DTOs at the platform edge.
package zenmind
