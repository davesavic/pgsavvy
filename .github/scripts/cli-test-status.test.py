import contextlib
import io
import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

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

    def test_temporary_failure_publishes_only_fixed_name_and_source_location(self):
        top = "TestTemporaryCLIIntegration"
        self.write_events([
            {"Action": "output", "Test": top + "/private-case-sentinel", "Output":
             "    /private-path-sentinel/pkg/cli/temporary_query_integration_test.go:171: private-password-sentinel SELECT private_sql\n"},
            {"Action": "fail", "Test": top + "/private-case-sentinel"},
        ])
        report = cli_test_status.read_status(self.path)
        self.assertEqual(report["tests"], [{"test": top, "status": "failed"}])
        self.assertEqual(report["cases"], [])
        self.assertEqual(report["locations"], [{"file": "temporary_query_integration_test.go", "line": 171}])
        self.assertNotIn("private-", json.dumps(report))

    def test_history_failure_reports_only_fixed_parent_name(self):
        name = "TestCLIHistoryProcess_OutputPipes"
        self.write_events([
            {"Action": "output", "Test": name, "Output": "private-output-sentinel"},
            {"Action": "fail", "Test": name + "/private-subtest-sentinel",
             "Package": "private-package-sentinel"},
            {"Action": "pass", "Test": name},
            {"Action": "fail", "Test": "TestCLIHistoryPrivateNameSentinel"},
        ])
        report = cli_test_status.read_status(self.path)
        self.assertEqual(report["log_status"], "parsed")
        self.assertEqual(report["tests"], [{"test": name, "status": "failed"}])
        self.assertTrue(report["unlisted_failure"])
        self.assertNotIn("private-", json.dumps(report))
        self.assertNotIn("PrivateNameSentinel", json.dumps(report))

    def test_new_history_cases_publish_only_fixed_labels_and_public_sites(self):
        families = {
            "TestCLIHistoryProcess_OutputPipes": (("broken_false", "broken_true"), 628),
            "TestCLIHistoryProcess_PrivacyAndOutput": (("backend_failure_false", "backend_failure_true"), 321),
        }
        for top, (labels, number) in families.items():
            for label in labels:
                with self.subTest(test=top, case=label):
                    self.write_events([
                        {"Action": "run", "Test": top + "/" + label},
                        {"Action": "output", "Test": top + "/" + label, "Output":
                         f"    Error Trace:\t/private-path-sentinel/pkg/cli/query_history_process_test.go:{number}\n"
                         "    private-assertion-sentinel SELECT private_sql PASSWORD_SENTINEL\n"},
                        {"Action": "fail", "Test": top + "/" + label},
                        {"Action": "pass", "Test": top + "/" + label},
                        {"Action": "fail", "Test": top + "/private-case-sentinel"},
                        {"Action": "output", "Test": top + "/" + label + "/private-nested-sentinel",
                         "Output": "    query_history_process_test.go:400: private-output-sentinel\n"},
                    ])
                    report = cli_test_status.read_status(self.path)
                    self.assertEqual(report["log_status"], "parsed")
                    self.assertEqual(report["tests"], [{"test": top, "status": "failed"}])
                    self.assertEqual(report["cases"], [{"test": top, "case": label, "action": "fail"}])
                    self.assertEqual(report["locations"], [{"file": "query_history_process_test.go", "line": number}])
                    for private in ("private-", "SELECT", "private_sql", "PASSWORD_SENTINEL", "pkg/"):
                        self.assertNotIn(private, json.dumps(report))

    def test_tls_snapshot_locations_do_not_publish_errors_or_dynamic_cases(self):
        top = "TestMachineOpen_TargetSnapshot_SSHTLS"
        invalid_line = cli_test_status.SOURCE_FILES["machine_test.go"][1] + 1
        self.write_events([
            {"Action": "run", "Test": top},
            {"Action": "output", "Test": top, "Output":
             "    /private-home-sentinel/pkg/drivers/pg/machine_test.go:934: private-TLS-error-sentinel\n"},
            {"Action": "output", "Test": top + "/private-case-sentinel", "Output":
             "    machine_test.go:943: private-SNI-sentinel\n"},
            {"Action": "output", "Test": top, "Output": f"    machine_test.go:{invalid_line}: private-line-sentinel\n"},
            {"Action": "fail", "Test": top},
        ])
        report = cli_test_status.read_status(self.path)
        self.assertEqual(report["log_status"], "parsed")
        self.assertEqual(report["tests"], [{"test": top, "status": "failed"}])
        self.assertEqual(report["cases"], [])
        self.assertEqual(report["locations"], [{"file": "machine_test.go", "line": 934}])
        self.assertNotIn("private-", json.dumps(report))

    def test_deadline_locations_keep_values_and_dynamic_cases_private(self):
        top = "TestMachineCLI_Envelope_ParentDeadlineAndInput"
        invalid_line = cli_test_status.SOURCE_FILES["dispatch_test.go"][1] + 1
        self.write_events([
            {"Action": "run", "Test": top},
            {"Action": "output", "Test": top, "Output":
             "    /private-path-sentinel/pkg/cli/dispatch_test.go:173: private-elapsed-sentinel private-JSON-sentinel\n"},
            {"Action": "output", "Test": top + "/private-case-sentinel", "Output":
             "    dispatch_test.go:176: private-output-sentinel\n"},
            {"Action": "output", "Test": top, "Output": f"    dispatch_test.go:{invalid_line}: private-line-sentinel\n"},
            {"Action": "fail", "Test": top},
        ])
        report = cli_test_status.read_status(self.path)
        self.assertEqual(report["log_status"], "parsed")
        self.assertEqual(report["tests"], [{"test": top, "status": "failed"}])
        self.assertEqual(report["cases"], [])
        self.assertEqual(report["locations"], [{"file": "dispatch_test.go", "line": 173}])
        self.assertNotIn("private-", json.dumps(report))

    def diagnostic_events(self):
        terminal = "TestMachineTerminalApprovalProcess"
        history = "TestCLIHistoryProcess_CancellationAndCleanup"
        return [
            {"Action": "run", "Test": terminal},
            {"Action": "run", "Test": terminal + "/idle"},
            {"Action": "output", "Test": terminal + "/idle", "Output":
             "    terminal_unix_test.go:348: private-output-sentinel SELECT private_sql\n"},
            {"Action": "output", "Output":
             "\t/private-path-sentinel/internal/machineio/stream_unix.go:58 +0x123\n"},
            {"Action": "output", "Output":
             "panic: private-panic-sentinel\nprivateFunction(private-argument-sentinel)\n"
             "\t/private-path-sentinel/internal/machineio/io_darwin_test.go:145 +0x123\n"},
            {"Action": "run", "Test": history},
            {"Action": "run", "Test": history + "/interrupt"},
            {"Action": "output", "Test": history + "/interrupt", "Output":
             "    Error Trace:\t/private-path-sentinel/pkg/cli/query_history_process_test.go:570\n"},
            {"Action": "fail", "Test": history + "/interrupt"},
            {"Action": "pass", "Test": history + "/interrupt"},
            {"Action": "fail", "Test": history},
        ]

    def test_fixed_cases_and_public_source_locations_diagnose_failure_and_hang(self):
        self.write_events(self.diagnostic_events())
        report = cli_test_status.read_status(self.path)
        self.assertEqual(report["log_status"], "parsed")
        self.assertEqual(report["cases"], [
            {"test": "TestCLIHistoryProcess_CancellationAndCleanup", "case": "interrupt", "action": "fail"},
            {"test": "TestMachineTerminalApprovalProcess", "case": "idle", "action": "run"},
        ])
        self.assertEqual(report["locations"], [
            {"file": "io_darwin_test.go", "line": 145},
            {"file": "query_history_process_test.go", "line": 570},
            {"file": "stream_unix.go", "line": 58},
            {"file": "terminal_unix_test.go", "line": 348},
        ])
        self.assertEqual(report["tests"], [
            {"test": "TestCLIHistoryProcess_CancellationAndCleanup", "status": "failed"},
            {"test": "TestMachineTerminalApprovalProcess", "status": "not_completed"},
        ])
        public = json.dumps(report)
        for private in ("private-", "private_sql", "SELECT", "+0x123", "internal/", "pkg/"):
            self.assertNotIn(private, public)

    def test_case_actions_distinguish_running_passing_failing_and_skipped(self):
        top = "TestMachineTerminalApprovalProcess"
        actions = {"idle": "run", "success": "pass", "deadline": "fail", "close": "skip"}
        self.write_events([{"Action": action, "Test": top + "/" + case}
                           for case, action in actions.items()])
        report = cli_test_status.read_status(self.path)
        self.assertEqual(report["cases"], [
            {"test": top, "case": case, "action": actions[case]} for case in sorted(actions)])

    def test_dynamic_cases_and_invalid_source_sites_stay_private(self):
        terminal = "TestMachineTerminalApprovalProcess"
        history = "TestCLIHistoryProcess_CancellationAndCleanup"
        events = [
            {"Action": "fail", "Test": terminal + "/private-case-sentinel"},
            {"Action": "fail", "Test": terminal + "/idle/private-nested-sentinel"},
            {"Action": "fail", "Test": history + "/interrupt#01"},
            {"Action": "fail", "Test": "TestMachineCLI_Dispatch/idle"},
        ]
        for site in ("stream_unix.go:0", "stream_unix.go:-1", "stream_unix.go:152",
                     "stream_unix.go:99999999999999", "stream_unix.go:58private-number-sentinel",
                     "stream_unix.go:058", "private-file-sentinel.go:58",
                     "/private-path-sentinel/stream_unix.go:58",
                     "private-stream_unix.go:58", "SELECT 'stream_unix.go:58'",
                     "query_history_process_test.go:9999",
                     *(f"{basename}:{last_line + 1}" for basename, (_, last_line)
                       in cli_test_status.SOURCE_FILES.items())):
            events.append({"Action": "output", "Test": terminal, "Output": "\t" + site + "\n"})
        events.append({"Action": "output", "Test": "TestMachineCLI_Dispatch",
                       "Output": "    stream_unix.go:58: private-unrelated-sentinel\n"})
        events.append({"Action": "output", "Test": terminal + "/idle/private-case-sentinel",
                       "Output": "    stream_unix.go:58: private-dynamic-sentinel\n"})
        self.write_events(events)
        report = cli_test_status.read_status(self.path)
        self.assertEqual(report["log_status"], "parsed")
        self.assertEqual(report["cases"], [])
        self.assertEqual(report["locations"], [])
        self.assertNotIn("private-", json.dumps(report))

    def test_bad_tail_discards_all_diagnostic_metadata(self):
        for tail, status in ((b"private-malformed-sentinel", "invalid"), (b"\xff", "invalid"),
                             (b'{"Action":"output","Output":{}}\n', "invalid"),
                             (b"x" * (cli_test_status.LIMIT + 1), "oversized")):
            with self.subTest(status=status):
                self.write_events(self.diagnostic_events() + [
                    {"Action": "fail"}, {"Action": "fail", "Test": "private-case-sentinel"}])
                with self.path.open("ab") as log:
                    log.write(tail)
                report = cli_test_status.read_status(self.path)
                self.assertEqual(report["log_status"], status)
                for field in ("tests", "cases", "locations"):
                    self.assertEqual(report[field], [])
                self.assertFalse(report["package_failed"])
                self.assertFalse(report["unlisted_failure"])
        self.write_events(self.diagnostic_events())
        with mock.patch.object(cli_test_status, "MAX_LOG_BYTES", self.path.stat().st_size - 1):
            report = cli_test_status.read_status(self.path)
        self.assertEqual(report["log_status"], "oversized")
        for field in ("tests", "cases", "locations"):
            self.assertEqual(report[field], [])

    def test_metadata_inventory_and_location_retention_are_bounded(self):
        events = []
        for top, labels in cli_test_status.CASE_NAMES.items():
            for label in labels:
                for action in ("run", "skip", "pass", "fail", "pass"):
                    events.append({"Action": action, "Test": top + "/" + label})
        for number in range(1, cli_test_status.MAX_LOCATIONS + 2):
            events.append({"Action": "output", "Output": f"\tstream_unix.go:{number} +0x123\n"})
        self.write_events(events * 2)
        report = cli_test_status.read_status(self.path)
        self.assertEqual(len(report["cases"]), sum(map(len, cli_test_status.CASE_NAMES.values())))
        self.assertEqual(len(report["cases"]), 23)
        self.assertTrue(all(case["action"] == "fail" for case in report["cases"]))
        self.assertEqual(report["locations"], [
            {"file": "stream_unix.go", "line": number}
            for number in range(1, cli_test_status.MAX_LOCATIONS + 1)])
        self.assertLess(len(json.dumps({key: report[key] for key in ("cases", "locations")})), 8192)

    def test_source_location_bounds_match_public_source(self):
        repo = pathlib.Path(__file__).resolve().parents[2]
        self.assertEqual({source for source, _ in cli_test_status.SOURCE_FILES.values()}, {
            "internal/machineio/terminal_unix_test.go", "internal/machineio/stream_unix.go",
            "internal/machineio/io_darwin_test.go", "pkg/cli/query_history_process_test.go",
            "pkg/drivers/pg/machine_test.go", "pkg/cli/dispatch_test.go",
            "pkg/cli/temporary_query_integration_test.go", "temporary_query_process_test.go"})
        for basename, (source, last_line) in cli_test_status.SOURCE_FILES.items():
            self.assertEqual(pathlib.PurePosixPath(source).name, basename)
            self.assertEqual(len((repo / source).read_text().splitlines()), last_line)

    def test_metadata_allowlists_match_public_source(self):
        repo = pathlib.Path(__file__).resolve().parents[2]
        self.assertEqual(cli_test_status.LOCATION_ONLY_TESTS,
                         {"TestMachineCLI_Envelope_ParentDeadlineAndInput",
                          "TestMachineOpen_TargetSnapshot_SSHTLS"})
        self.assertLessEqual(cli_test_status.LOCATION_ONLY_TESTS, cli_test_status.TEST_NAMES)
        terminal = (repo / "internal/machineio/terminal_unix_test.go").read_text()
        terminal = terminal.split("func TestMachineTerminalApprovalProcess(", 1)[1]
        modes = re.search(r"for _, mode := range \[\]string\{([^}]+)\}", terminal)[1]
        self.assertEqual(cli_test_status.CASE_NAMES["TestMachineTerminalApprovalProcess"],
                         {"wait-invalid", *re.findall(r'"([^"]+)"', modes)})
        history = (repo / "pkg/cli/query_history_process_test.go").read_text()
        history = history.split("func TestCLIHistoryProcess_CancellationAndCleanup(", 1)[1]
        history = history.split("\nfunc ", 1)[0]
        self.assertIn("[]os.Signal{os.Interrupt, syscall.SIGTERM}", history)
        fixtures = re.search(r"for _, fixture := range \[\]string\{([^}]+)\}", history)[1]
        self.assertEqual(cli_test_status.CASE_NAMES["TestCLIHistoryProcess_CancellationAndCleanup"],
                         {"interrupt", "terminated", *re.findall(r'"([^"]+)"', fixtures)})
        source = (repo / "pkg/cli/query_history_process_test.go").read_text()
        for top, variable, template in (
                ("TestCLIHistoryProcess_OutputPipes", "broken", "broken_%t"),
                ("TestCLIHistoryProcess_PrivacyAndOutput", "failure", "backend_failure_%t")):
            body = source.split("func " + top + "(", 1)[1].split("\nfunc ", 1)[0]
            self.assertIn("for _, " + variable + " := range []bool{false, true}", body)
            self.assertIn('t.Run(fmt.Sprintf("' + template + '", ' + variable + ')', body)
            self.assertEqual(cli_test_status.CASE_NAMES[top],
                             {template.replace("%t", value) for value in ("false", "true")})

    def test_aggregate_output_over_one_mib_keeps_private_failure_status(self):
        name = "TestCLIHistoryProcess_CancellationAndCleanup"
        output_event = {"Action": "output", "Test": name,
                        "Output": "private-output-sentinel" * 200}
        self.write_events([
            {"Action": "run", "Test": name},
            {"Action": "run", "Test": name + "/interrupt"},
            *[output_event] * 300,
            {"Action": "fail", "Test": name + "/interrupt"},
            {"Action": "output", "Test": name + "/interrupt",
             "Output": "    query_history_process_test.go:570: private-output-sentinel\n"},
            {"Action": "fail", "Test": name + "/private-subtest-sentinel",
             "Package": "private-package-sentinel"},
            {"Action": "fail", "Test": "TestMachinePrivateNameSentinel"},
            {"Action": "fail", "Package": "private-package-sentinel"},
        ])
        self.assertGreater(self.path.stat().st_size, cli_test_status.LIMIT)
        report = cli_test_status.read_status(self.path)
        self.assertEqual(report["log_status"], "parsed")
        self.assertEqual(report["tests"], [{"test": name, "status": "failed"}])
        self.assertEqual(report["cases"], [{"test": name, "case": "interrupt", "action": "fail"}])
        self.assertEqual(report["locations"], [{"file": "query_history_process_test.go", "line": 570}])
        self.assertTrue(report["package_failed"])
        self.assertTrue(report["unlisted_failure"])
        public = json.dumps(report)
        self.assertNotIn("private-", public)
        self.assertNotIn("PrivateNameSentinel", public)
        self.assertNotIn(str(self.path), public)
        with self.path.open("ab") as log:
            log.write(b"private-malformed-tail")
        report = cli_test_status.read_status(self.path)
        self.assertEqual(report["log_status"], "invalid")
        self.assertEqual(report["tests"], [])
        self.assertEqual(report["cases"], [])
        self.assertEqual(report["locations"], [])
        self.assertFalse(report["package_failed"])
        self.assertFalse(report["unlisted_failure"])

    def test_incomplete_input_fails_closed(self):
        for raw in (b"{private-parse-sentinel", b"\xff", b"[]\n", b'{"Action": 1}\n',
                    b'{"Action": "fail", "Test": 1}\n', b"[" * 2000):
            with self.subTest(raw=raw):
                self.path.write_bytes(raw)
                report = cli_test_status.read_status(self.path)
                self.assertEqual(report["log_status"], "invalid")
                self.assertEqual(report["tests"], [])
        self.write_events([{"Action": "pass", "Test": "TestMachineCLI_Dispatch"}])
        with self.path.open("ab") as log:
            log.write(b"private-malformed-tail")
        self.assertEqual(cli_test_status.read_status(self.path)["tests"], [])

    def test_stream_reads_and_total_scan_are_bounded(self):
        event = json.dumps({"Action": "pass", "Test": "TestMachineCLI_Dispatch"}).encode() + b"\n"
        for suffix in (b"", b"private-unscanned-sentinel"):
            with self.subTest(oversized=bool(suffix)):
                stream = io.BytesIO(event * 2 + suffix)
                stream.readline = mock.Mock(wraps=stream.readline)
                stream.read = mock.Mock(side_effect=AssertionError("whole-log read"))
                with mock.patch.object(cli_test_status, "LIMIT", len(event)), \
                        mock.patch.object(cli_test_status, "MAX_LOG_BYTES", 2 * len(event)), \
                        mock.patch.object(pathlib.Path, "open", return_value=stream):
                    report = cli_test_status.read_status(self.path)
                self.assertEqual(stream.readline.call_args_list, [
                    mock.call(len(event) + 1), mock.call(len(event) + 1), mock.call(1)])
                self.assertEqual(report["log_status"], "oversized" if suffix else "parsed")
                self.assertEqual(report["tests"], [] if suffix else [
                    {"test": "TestMachineCLI_Dispatch", "status": "passed"}])
                self.assertFalse(report["package_failed"])
                self.assertFalse(report["unlisted_failure"])

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
        self.path.write_bytes(raw + b"\n" + b"x" * (cli_test_status.LIMIT + 1))
        report = cli_test_status.read_status(self.path)
        self.assertEqual(report["log_status"], "oversized")
        self.assertEqual(report["tests"], [])

    def test_allowlist_matches_committed_source_names(self):
        repo = pathlib.Path(__file__).resolve().parents[2]
        names = set()
        for directory in (".", "internal/machineio", "pkg/cli", "pkg/drivers/pg",
                          "pkg/session", "pkg/session/sshtunnel", "test/integration"):
            for path in (repo / directory).glob("*_test.go"):
                names.update(re.findall(r"^func ((?:TestMachine|TestCLIHistory|TestTemporary)\w*)\(",
                                        path.read_text(), re.MULTILINE))
        self.assertEqual(cli_test_status.TEST_NAMES, names)

    def test_temporary_inventory_is_required_and_skip_is_not_a_live_pass(self):
        repo = pathlib.Path(__file__).resolve().parents[2]
        source = (repo / ".github/scripts/cli-postgres.sh").read_text()
        self.assertIn("native_selector='^(TestMachine|TestCLIHistory|TestTemporary)'", source)
        evidence = source.split('from cli_test_status import TEST_NAMES\n', 1)[1]
        evidence = evidence.split('binary = root / "pgsavvy"', 1)[0]
        # Execute the shipping acceptance logic, not a reimplementation. This
        # fragment stops before binary hashing/platform reporting.
        script = "import json, pathlib, re, sys\nfrom cli_test_status import TEST_NAMES\n" + evidence
        required = set(re.findall(r'"(Test\w+)"', evidence.split("inventory = set()", 1)[0]))
        temporary = {
            "TestTemporaryAtomicPostgres", "TestTemporaryCLIIntegration", "TestTemporaryCLILexicalPostgres",
            "TestTemporaryCLIProcessIsolation", "TestTemporaryCLIProcessCommitInterruption",
            "TestTemporaryCLIProcessBrokenStdout",
            "TestTemporaryCLIObserverOwnership", "TestTemporaryCLIProcessObserverOwnership",
        }
        self.assertLessEqual(temporary, required)
        baseline = {"TestMachineCLI_" + name for name in (
            "Dispatch", "ExitCodes", "Envelope", "Envelope_ParentDeadlineAndInput", "OutputLimit", "BrokenWriter")}
        required |= baseline | {"TestMachineApprovalCeremony", "TestCLIHistory_DefaultNoState"}
        package = "github.com/davesavic/pgsavvy/pkg/cli"
        events = [{"Action": "pass", "Test": name, "Package": package} for name in sorted(required)]
        events += [
            {"Action": "output", "Test": "TestTemporaryCLIProcessCommitInterruption", "Output": "interrupted COMMIT attempts=1, reported=unknown, eventual observer rows=0 (not prescribed)"},
            {"Action": "output", "Test": "TestTemporaryCLIProcessBrokenStdout", "Output": "broken stdout exit=7; independent observer durable rows=1 attempts=1"},
        ]
        events += [{"Action": "output", "Test": "TestTemporaryCLIIntegration/fixed-case",
                    "Output": f"rollback proof: id={number} marker_attempts=1 backend_terminated=true rows=0"}
                   for number in range(1010, 1015)]
        root = pathlib.Path(self.directory.name)
        def write(name, records):
            (root / name).write_text("".join(json.dumps(record) + "\n" for record in records))
        write("tests.jsonl", [{"Action": "pass", "Test": name, "Package": package} for name in baseline])
        write("native-inventory.jsonl", [{"Action": "output", "Output": name + "\n", "Package": package} for name in required])
        for mode in ("pass", "skip-subtest", "missing-compiled-test", "missing-required-test"):
            records = list(events)
            if mode == "skip-subtest":
                records.append({"Action": "skip", "Test": "TestTemporaryCLIIntegration/private-case", "Package": package})
            if mode == "missing-compiled-test":
                write("native-inventory.jsonl", [{"Action": "output", "Output": name + "\n", "Package": package} for name in required | {"TestTemporaryTarget_Validation"}])
            if mode == "missing-required-test":
                write("native-inventory.jsonl", [{"Action": "output", "Output": name + "\n", "Package": package} for name in required])
                records = [event for event in records if event["Test"] != "TestTemporaryAtomicPostgres"]
            write("native-tests.jsonl", records)
            with self.subTest(mode=mode):
                result = subprocess.run([sys.executable, "-c", script, str(root)], cwd=repo / ".github/scripts", capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, mode == "pass", result.stderr)

    def test_owned_observer_metadata_is_private_and_binds_endpoint_and_cluster(self):
        repo = pathlib.Path(__file__).resolve().parents[2]
        source = (repo / ".github/scripts/cli-postgres.sh").read_text()
        writer = source.split("stage=temporary-ownership\n", 1)[1].split("<<'PY'\n", 1)[1].split("\nPY\n", 1)[0]
        root = pathlib.Path(self.directory.name) / "pgsavvy-native.metadata"
        (root / "data").mkdir(parents=True)
        (root / "temporary-identity").write_text(f"1234567890123456789|{root / 'data'}\n")
        dsn = "postgres://pgsavvy:private-fixture@127.0.0.1:65432/pgsavvy_test?sslmode=disable"
        environment = {**os.environ, "PGSAVVY_TEMP_FIXTURE_TOKEN": "private-token", "PGSAVVY_TEST_PG": dsn}
        result = subprocess.run([sys.executable, "-c", writer, str(root)], env=environment, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "")
        path = root / "temporary-owner"
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(json.loads(path.read_text()), {
            "token": "private-token", "observer_dsn": dsn,
            "system_identifier": "1234567890123456789", "data_directory": str((root / "data").resolve())})


if __name__ == "__main__":
    unittest.main()
