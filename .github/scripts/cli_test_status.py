import json
import pathlib
import sys

LIMIT = 1024 * 1024
MAX_LOG_BYTES = 64 * LIMIT

# Fixed committed-source names, never names supplied only by private test output.
TEST_NAMES = frozenset("""
TestCLIHistoryLiveProcessHelper
TestCLIHistoryProcess_CancellationAndCleanup
TestCLIHistoryProcess_ConcurrentStartup
TestCLIHistoryProcess_DefaultNoState
TestCLIHistoryProcess_ExitPersistence
TestCLIHistoryProcess_Helper
TestCLIHistoryProcess_OutputPipes
TestCLIHistoryProcess_PrivacyAndOutput
TestCLIHistoryProcess_SQLBoundary
TestCLIHistoryProcess_StoreFailures
TestCLIHistory_BudgetLedger
TestCLIHistory_ConsentSnapshotAndExhaustion
TestCLIHistory_DefaultNoState
TestCLIHistory_ExternalCancellation
TestCLIHistory_FullApprovalCannotEnableConsent
TestCLIHistory_FullApprovalFlagsPrivacy
TestCLIHistory_Live
TestCLIHistory_LockedStore
TestCLIHistory_MetadataBoundaries
TestCLIHistory_MetadataOnlyRejection
TestCLIHistory_PublicationAndIsolation
TestCLIHistory_QueryOutcomes
TestCLIHistory_RollbackRehearsal
TestCLIHistory_WarningsPreserveExit
TestMachineAgent_DeadlineAndClose
TestMachineAgent_EndpointPrecedence
TestMachineAgent_StalledProtocol
TestMachineAgent_Transport
TestMachineApprovalCancellation
TestMachineApprovalCeremony
TestMachineApprovalIndependentReusable
TestMachineApprovalPreview
TestMachineApprovalUnavailable
TestMachineApprovalWriteLifecycle
TestMachineCLIBatchAnalysisPostgresDifferential
TestMachineCLIFullApprovalProcess
TestMachineCLIFullPostgresTerminal
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
TestMachineCLI_EnvelopeLegacyBytes
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
TestMachineExecuteClosePreservesOutcome
TestMachineExecuteDrainLimits
TestMachineExecuteInitialization
TestMachineExecuteNativeBatch
TestMachineExecuteOperationOwnership
TestMachineExecuteParameters
TestMachineExecutePolicy
TestMachineExecutePostgresCancelCommit
TestMachineExecutePostgresLimits
TestMachineExecutePostgresPolicyAndMaintenance
TestMachineExecutePostgresTransactions
TestMachineExecuteRollbackBounded
TestMachineExecuteRowBounds
TestMachineExecuteSetupFailure
TestMachineExecuteTerminalOutcomes
TestMachineExecuteValidation
TestMachineFullAccessPolicy
TestMachineFullAccessPolicy_Concurrent
TestMachineFullAccessPolicy_Gates
TestMachineFullAccessPolicy_NetworkPrivacy
TestMachineFullAccessPolicy_Service
TestMachineFullOutputApprovalCodes
TestMachineFullOutputBrokenWriterAndSignalAfterPublication
TestMachineFullOutputCancellationAndDeadlines
TestMachineFullOutputEncodingBoundary
TestMachineFullOutputErrorRetention
TestMachineFullOutputResults
TestMachineFullOutputStatusAllowlist
TestMachineFullQueryProcessDeliveryAndDeadline
TestMachineFullQueryProcessRestrictedPolicy
TestMachineFullQueryProcessTransactions
TestMachineFullQuery_ApprovalFlags
TestMachineFullQuery_ApprovalFlagsQueryOnly
TestMachineFullQuery_ApprovalHistory
TestMachineFullQuery_ApprovalMatrix
TestMachineFullQuery_CancelApproval
TestMachineFullQuery_CapturedSnapshot
TestMachineFullQuery_DesignatedPostgres
TestMachineFullQuery_ExplicitFlagsCannotElevate
TestMachineFullQuery_OfflineDiscovery
TestMachineFullQuery_OriginalRoutingAndResults
TestMachineFullQuery_Preflight
TestMachineFullQuery_PreflightParameters
TestMachineFullQuery_PublicationPreservesOutcome
TestMachineFullQuery_ReadOnlyOverride
TestMachineFullQuery_ReusableConcurrentCommand
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
TestMachineLLMHelp_CanonicalCapabilities
TestMachineLLMHelp_Dispatch
TestMachineLLMHelp_Grammar
TestMachineLLMHelp_GuideAndExamples
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
TestMachineTerminalApprovalCanceled
TestMachineTerminalApprovalLock
TestMachineTerminalApprovalProcess
TestMachineTerminalApprovalUnsafeLock
TestMachineTerminalApprovalUnsafeNamespace
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
            report["log_status"] = "invalid"
            statuses = {}
            package_failed = unlisted_failure = saw_event = False
            total = 0
            while True:
                # Large suites can exceed one record's budget; never retain the whole log.
                line = log.readline(min(LIMIT, MAX_LOG_BYTES - total) + 1)
                if not line:
                    break
                total += len(line)
                if len(line) > LIMIT or total > MAX_LOG_BYTES:
                    report["log_status"] = "oversized"
                    return report
                if not line.strip():
                    continue
                event = json.loads(line.decode("utf-8"))
                if not isinstance(event, dict) or not isinstance(event.get("Action"), str):
                    return report
                saw_event = True
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
            if not saw_event:
                return report
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
