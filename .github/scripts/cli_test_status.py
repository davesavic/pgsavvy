import json
import pathlib
import sys

LIMIT = 1024 * 1024

# Fixed committed-source names, never names supplied only by private test output.
TEST_NAMES = frozenset("""
TestMachineAgent_DeadlineAndClose
TestMachineAgent_EndpointPrecedence
TestMachineAgent_StalledProtocol
TestMachineAgent_Transport
TestMachineCLINative_Discovery
TestMachineCLINative_GroupSignal
TestMachineCLINative_HelperExecutable
TestMachineCLINative_InvalidInputHandle
TestMachineCLINative_ProfilePolicyAndInput
TestMachineCLINative_ShellPipe
TestMachineCLIProcess_Discovery
TestMachineCLIProcess_LegacyRoutes
TestMachineCLIProcess_Query
TestMachineCLIProcess_Query_OutputPipes
TestMachineCLIProcess_Schema
TestMachineCLIProcess_Signals
TestMachineCLIProcess_StreamFlags
TestMachineCLIProcess_TargetSelection
TestMachineCLIProcess_Test
TestMachineCLIUnsupportedKeepsLegacyRoute
TestMachineCLI_BrokenWriter
TestMachineCLI_Dispatch
TestMachineCLI_Envelope
TestMachineCLI_Envelope_ParentDeadlineAndInput
TestMachineCLI_ExitCodes
TestMachineCLI_OutputLimit
TestMachineCLI_WindowsPipeDeadlineCapability
TestMachineConfig_IsolatedEnvironment
TestMachineConfig_NativeDefaults
TestMachineConfig_TargetSelection
TestMachineConnections_Capabilities
TestMachineConnections_Grammar
TestMachineConnections_OfflineProfiles
TestMachineCredentialHelperProcess
TestMachineCredentialHelperProcess_Unix
TestMachineCredentialHelperRestrictedParentProcess
TestMachineCredentials_EffectiveTarget
TestMachineCredentials_EffectiveTarget_ParsedFallbacks
TestMachineCredentials_HelperLimits
TestMachineCredentials_HelperLimits_ConcurrentRunning
TestMachineCredentials_HelperLimits_Descendant
TestMachineCredentials_KeyringExactIdentity
TestMachineCredentials_NoPrompt
TestMachineCredentials_Precedence
TestMachineIO_BlockingFIFO
TestMachineIO_BlockingSocketOutput
TestMachineIO_Cancellation
TestMachineIO_CleanupPeerFirst
TestMachineIO_DarwinFIFOEOF
TestMachineIO_FIFOResources
TestMachineIO_KqueueDescriptorInventory
TestMachineIO_MetadataDescriptorRejected
TestMachineIO_Ownership
TestMachineIO_PTYMasterRejected
TestMachineIO_PipeFlags
TestMachineIO_PipeRejection
TestMachineIO_PollTimeoutRetry
TestMachineIO_ProcessAcquisitionLifetime
TestMachineIO_ProcessCancellation
TestMachineIO_ProcessClosedStandardSlots
TestMachineIO_ProcessHelperPrivate
TestMachineIO_ProcessIndependentInvocations
TestMachineIO_ProcessOutputAcknowledged
TestMachineIO_ProcessOwnership
TestMachineIO_ProcessProtocolFailure
TestMachineIO_ProcessSocketCancellation
TestMachineIO_ProcessStartupCancellation
TestMachineIO_Sockets
TestMachineIO_TerminalFlags
TestMachineInput_CancellationJoinsWatcher
TestMachineInput_ConcurrentCancellation
TestMachineInput_FileBoundsAndErrors
TestMachineInput_ReadFailure
TestMachineInput_UnixDenied
TestMachineInput_UnixNonregular
TestMachineInput_WindowsPaths
TestMachineMetadata_Describe
TestMachineMetadata_Describe_Live
TestMachineMetadata_EmbeddedSQL
TestMachineMetadata_Failures
TestMachineMetadata_Failures_Aggregates
TestMachineMetadata_Failures_ConcurrentDDL_Live
TestMachineMetadata_Failures_CumulativeBudget
TestMachineMetadata_Failures_Live
TestMachineMetadata_Indexes
TestMachineMetadata_Indexes_Live
TestMachineMetadata_Paging
TestMachineMetadata_Paging_Live
TestMachineMetadata_Relationships
TestMachineMetadata_Relationships_Live
TestMachineMetadata_TargetSelection_Live
TestMachineOpen_Initialization
TestMachineOpen_Lifecycle
TestMachineOpen_Lifecycle_ClosePublication
TestMachineOpen_Lifecycle_Concurrent
TestMachineOpen_Lifecycle_RollbackAndCloseBudget
TestMachineOpen_Lifecycle_TLSNegotiationTimeoutFailover
TestMachineOpen_Lifecycle_TimeoutFailoverAndJoinedStartup
TestMachineOpen_PolicyOrder
TestMachineOpen_Privacy
TestMachineOpen_TargetSnapshot
TestMachineOpen_TargetSnapshot_Failover
TestMachineOpen_TargetSnapshot_SSHChannelTimeoutFailover
TestMachineOpen_TargetSnapshot_SSHDNS
TestMachineOpen_TargetSnapshot_SSHStartupTimeoutFailover
TestMachineOpen_TargetSnapshot_SSHTLS
TestMachinePgpass_CheckedHandle
TestMachinePgpass_Privacy
TestMachinePgpass_WindowsACL
TestMachinePgpass_WindowsDescriptor
TestMachineProfiles_Allowlist
TestMachineProfiles_LoadRegularFile
TestMachineProfiles_OpenChecks
TestMachineProfiles_RegularFile
TestMachineQuery_Bounds
TestMachineQuery_Bounds_Input
TestMachineQuery_Bounds_Live
TestMachineQuery_Cancel
TestMachineQuery_Cancel_Input
TestMachineQuery_Cancel_Live
TestMachineQuery_Parameters
TestMachineQuery_Policy
TestMachineQuery_Policy_Live
TestMachineQuery_Policy_NestedAST
TestMachineQuery_Policy_StaleFormRevocation
TestMachineQuery_TerminalErrors
TestMachineQuery_TerminalErrors_Live
TestMachineQuery_TextValues
TestMachineQuery_TextValues_Live
TestMachineSSH_AgentCancellation
TestMachineSSH_Auth
TestMachineSSH_Auth_AgentThenIdentity
TestMachineSSH_Auth_HelperCancellationAndBounds
TestMachineSSH_Auth_StrictTrustBeforeHelpers
TestMachineSSH_LegacyTOFU
TestMachineSSH_OwnedAbort
TestMachineSSH_OwnedAbort_ChannelOpenAndWrite
TestMachineSSH_OwnedAbort_Handshake
TestMachineSSH_Trust
TestMachineSchema_Access
TestMachineSchema_Access_ConcurrentProcesses_Live
TestMachineSchema_DTOs
TestMachineSchema_DTOs_Live
TestMachineSchema_Failures
TestMachineSchema_Failures_Live
TestMachineSchema_Failures_StalledCatalog_Live
TestMachineSchema_Grammar
TestMachineSchema_Pagination
TestMachineSchema_Pagination_Live
TestMachineWindowsHelper_CommandSemantics
TestMachineWindowsHelper_Containment
TestMachineWindowsHelper_Descendants
TestMachineWindowsHelper_EnvironmentBlock
TestMachineWindowsHelper_IndependentInvocations
TestMachineWindowsHelper_InvalidSetup
""".split())


def read_status(path):
    report = {"scope": "native-test-status", "log_status": "unavailable",
              "tests": [], "package_failed": False, "unlisted_failure": False}
    try:
        with pathlib.Path(path).open("rb") as log:
            raw = log.read(LIMIT + 1)
        report["log_status"] = "invalid"
        if len(raw) > LIMIT:
            report["log_status"] = "oversized"
            return report
        if not raw.strip():
            return report
        statuses = {}
        package_failed = unlisted_failure = False
        for line in raw.decode("utf-8").splitlines():
            if not line.strip():
                continue
            event = json.loads(line)
            if not isinstance(event, dict) or not isinstance(event.get("Action"), str):
                return report
            action, name = event["Action"], event.get("Test")
            if name is None:
                package_failed |= action == "fail"
                continue
            if not isinstance(name, str):
                return report
            top = name.partition("/")[0]
            if top not in TEST_NAMES:
                unlisted_failure |= action == "fail"
                continue
            if action == "fail":
                statuses[top] = "failed"
            elif name == top and statuses.get(top) != "failed":
                if action == "pass":
                    statuses[top] = "passed"
                elif action in ("run", "skip"):
                    statuses[top] = "not_completed"
        report.update(log_status="parsed", package_failed=package_failed,
                      unlisted_failure=unlisted_failure,
                      tests=[{"test": name, "status": statuses[name]} for name in sorted(statuses)])
    except (OSError, UnicodeError, ValueError, TypeError, RecursionError):
        pass
    return report


def main(path):
    print(json.dumps(read_status(path)))


if __name__ == "__main__":
    main(sys.argv[1])
