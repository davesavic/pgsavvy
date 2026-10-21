import contextlib
import io
import json
import pathlib
import re
import tempfile
import unittest

import cli_test_status


class TestNativeTestStatus(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = pathlib.Path(self.directory.name) / "private.jsonl"

    def write_events(self, events):
        self.path.write_text("".join(json.dumps(event) + "\n" for event in events))

    def test_only_allowlisted_top_level_names_escape(self):
        name = "TestMachineCLIProcess_Query"
        self.write_events([
            {"Action": "output", "Test": name, "Output": "private-output-sentinel"},
            {"Action": "fail", "Test": name + "/private-subtest-sentinel", "Package": "private-package-sentinel"},
            {"Action": "pass", "Test": name},
            {"Action": "fail", "Test": "TestMachinePrivateNameSentinel"},
            {"Action": "fail", "Package": "private-package-sentinel"},
        ])
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            cli_test_status.main(self.path)
        public = output.getvalue()
        self.assertNotIn("private-", public)
        self.assertNotIn("PrivateNameSentinel", public)
        self.assertNotIn(str(self.path), public)
        report = json.loads(public)
        self.assertEqual(report["log_status"], "parsed")
        self.assertEqual(report["tests"], [{"test": name, "status": "failed"}])
        self.assertTrue(report["package_failed"])
        self.assertTrue(report["unlisted_failure"])

    def test_pass_and_skip(self):
        self.write_events([
            {"Action": "pass", "Test": "TestMachineCLI_Dispatch"},
            {"Action": "skip", "Test": "TestMachineCLI_Envelope"},
        ])
        self.assertEqual(cli_test_status.read_status(self.path)["tests"], [
            {"test": "TestMachineCLI_Dispatch", "status": "passed"},
            {"test": "TestMachineCLI_Envelope", "status": "not_completed"},
        ])

    def test_incomplete_input_fails_closed(self):
        for raw in (b"{private-parse-sentinel", b"\xff", b"[]\n", b'{"Action": 1}\n'):
            with self.subTest(raw=raw):
                self.path.write_bytes(raw)
                report = cli_test_status.read_status(self.path)
                self.assertEqual(report["log_status"], "invalid")
                self.assertEqual(report["tests"], [])
        self.write_events([{"Action": "pass", "Test": "TestMachineCLI_Dispatch"}])
        with self.path.open("ab") as log:
            log.write(b"private-malformed-tail")
        self.assertEqual(cli_test_status.read_status(self.path)["tests"], [])

    def test_missing_empty_and_oversized(self):
        self.assertEqual(cli_test_status.read_status(self.path)["log_status"], "unavailable")
        self.path.write_bytes(b"")
        self.assertEqual(cli_test_status.read_status(self.path)["log_status"], "invalid")
        raw = json.dumps({"Action": "pass", "Test": "TestMachineCLI_Dispatch"}).encode()
        self.path.write_bytes(raw.ljust(cli_test_status.LIMIT, b" "))
        self.assertEqual(cli_test_status.read_status(self.path)["log_status"], "parsed")
        self.path.write_bytes(b"x" * (cli_test_status.LIMIT + 1))
        report = cli_test_status.read_status(self.path)
        self.assertEqual(report["log_status"], "oversized")
        self.assertEqual(report["tests"], [])

    def test_allowlist_matches_committed_source_names(self):
        repo = pathlib.Path(__file__).resolve().parents[2]
        names = set()
        for directory in (".", "internal/machineio", "pkg/cli", "pkg/drivers/pg",
                          "pkg/session", "pkg/session/sshtunnel", "test/integration"):
            for path in (repo / directory).glob("*_test.go"):
                names.update(re.findall(r"^func (TestMachine\w+)\(", path.read_text(), re.MULTILINE))
        self.assertEqual(cli_test_status.TEST_NAMES, names)


if __name__ == "__main__":
    unittest.main()
