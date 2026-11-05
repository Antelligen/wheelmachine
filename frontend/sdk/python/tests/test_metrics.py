"""What a machine used: asked for from a time or how long ago, in steps, and read into objects - None for a number
that could not be read."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone

from support import APITest

from wheelmachine import BadRequestError, MachineMetrics

POINTS = [
    {"at": "2026-10-23T12:03:00Z", "cpu_percent": 12.5, "memory_bytes": 536870912, "memory_limit_bytes": 4294967296,
     "disk_read_bps": 1536.0, "disk_write_bps": 300.0, "net_rx_bps": 10240.0, "net_tx_bps": 2048.0},
    {"at": "2026-10-23T12:04:00Z", "cpu_percent": None, "memory_bytes": 1073741824, "memory_limit_bytes": None,
     "disk_read_bps": None, "disk_write_bps": None, "net_rx_bps": None, "net_tx_bps": None},
]


class Metrics(APITest):
    def test_points_are_read_with_what_could_not_be_read_as_none(self) -> None:
        m = self.api.add_machine("web")
        self.api.metrics[m["id"]] = POINTS
        got = self.client.machines.metrics(m["id"])
        self.assertIsInstance(got, MachineMetrics)
        self.assertEqual((got.machine, got.step_s, got.cpus, got.kept_since), (m["id"], 60, 2.0, "2026-10-23T12:03:00Z"))
        first, second = got.points
        self.assertEqual((first.at, first.cpu_percent, first.memory_bytes, first.net_tx_bps), ("2026-10-23T12:03:00Z", 12.5, 536870912.0, 2048.0))
        self.assertEqual((second.cpu_percent, second.memory_limit_bytes, second.disk_read_bps), (None, None, None))
        self.assertEqual(second.memory_bytes, 1073741824.0)
        self.assertEqual(self.api.requests("GET", "/metrics$")[-1].query, {})
        self.assertEqual(self.client.machines.metrics(self.api.add_machine("idle")["id"]).points, [])

    def test_from_when_and_in_what_steps(self) -> None:
        m = self.api.add_machine("web")
        asked = lambda: self.api.requests("GET", "/metrics$")[-1].query  # noqa: E731
        self.client.machines.metrics(m["id"], since="6h", step=300)
        self.assertEqual(asked(), {"since": ["6h"], "step": ["300"]})
        self.client.machines.metrics(m["id"], since=timedelta(hours=2), step=timedelta(minutes=5))
        self.assertEqual(asked(), {"since": ["7200s"], "step": ["300"]})
        self.client.machines.metrics(m["id"], since=datetime(2026, 10, 23, 9, 0, tzinfo=timezone.utc))
        self.assertEqual(asked(), {"since": ["2026-10-23T09:00:00Z"]})
        with self.assertRaises(ValueError):
            self.client.machines.metrics(m["id"], since=datetime(2026, 10, 23, 9, 0))
        with self.assertRaises(BadRequestError):
            self.client.machines.metrics(m["id"], step=45)
