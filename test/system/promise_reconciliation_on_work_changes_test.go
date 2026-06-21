package system_test

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/test/kubeutils"
)

var _ = Describe("Promise Reconciliation on Work Changes", Label("destination", "promise-reconciliation"), Serial,
	func() {
		const (
			// Crossplane Promise name for the test.
			crossplanePromiseName = "crossplane"
			// Crossplane Promise URL for the test.
			crossplanePromiseURL = "https://raw.githubusercontent.com/syntasso/kratix-marketplace/refs/heads/main/crossplane/promise.yaml"

			// Crossplane's required destination label key for scheduling Works created by the Crossplane Promise.
			crossplaneLabelKey = "crossplane"
			// Crossplane's required destination label value for scheduling Works created by the Crossplane Promise.
			crossplaneLabelValue = "enabled"

			// WorkPlacement selector key used for identifying WorkPlacements created for the Crossplane Promise.
			// The value for this label key is set to the Work name created for the Promise.
			workLabelKey = "kratix.io/work"

			// Promise condition types.
			conditionTypeReconciled     = "Reconciled"
			conditionTypeWorksSucceeded = "WorksSucceeded"

			// Promise condition reasons.
			conditionReasonWorkflowPending = "WorkflowPending"
			conditionReasonWorksPending    = "WorksPending"
			conditionReasonReconciled      = "Reconciled"
			conditionReasonWorksSucceeded  = "WorksSucceeded"

			// Promise condition statuses.
			conditionStatusUnknown = "Unknown"
			conditionStatusTrue    = "True"

			// Promise status(es).
			promiseStatusAvailable = "Available"

			// Unit Separator (US) control character used as a delimiter when packing multiple fields into one output.
			fieldDelimiter = "\u001F"
		)

		var (
			// Destination label argument format used by kubectl label to remove the key: "crossplane-"
			crossplaneLabelUnset = fmt.Sprintf("%s-", crossplaneLabelKey)
			// Destination label argument format used by kubectl label to set key-value: "crossplane=enabled"
			crossplaneLabelSet = fmt.Sprintf("%s=%s", crossplaneLabelKey, crossplaneLabelValue)
		)

		// This is an asynchronous multi-cluster flow (Promise reconcile, Work scheduling, WorkPlacement creation),
		// so we use longer timeouts and steady polling to avoid flakes from normal reconciliation latency.
		// Set both Ginkgo defaults and kubeutils defaults so Eventually checks are consistent everywhere in this test.
		BeforeEach(func() {
			SetDefaultEventuallyTimeout(5 * time.Minute)
			SetDefaultEventuallyPollingInterval(2 * time.Second)
			kubeutils.SetTimeoutAndInterval(5*time.Minute, 2*time.Second)
		})

		// Test for https://github.com/syntasso/kratix/issues/777
		//
		// The Crossplane Promise requires the destination label "crossplane=enabled" to schedule its Work. This test
		// validates that the Crossplane Promise conditions transition correctly.
		//
		// - Before adding label:
		//   {
		//     "Reconciled": {"reason": "WorkflowPending", "status": "Unknown"},
		//     "WorksSucceeded": {"reason": "WorksPending", "status": "Unknown"}
		//   }
		//
		// - After adding label:
		//   {
		//     "Reconciled": {"reason": "Reconciled", "status": "True"},
		//     "WorksSucceeded": {"reason": "WorksSucceeded", "status": "True"}
		//   }
		It("transitions Crossplane Promise from pending works to reconciled once destination label is added", func() {
			// Selector for resources created by the Crossplane Promise.
			selector := fmt.Sprintf("kratix.io/promise-name=%s", crossplanePromiseName)

			// Start from a clean state by deleting stale Crossplane WorkPlacement resources from earlier runs.
			By("cleaning up any previous Crossplane WorkPlacement resources")
			platform.KubectlAllowFail(
				"delete", "workplacement",
				"-n", v1alpha1.SystemNamespace,
				"-l", selector,
				"--ignore-not-found=true",
			)

			// Start from a clean state by deleting stale Crossplane Work resources from earlier runs.
			By("cleaning up any previous Crossplane Work resources")
			platform.KubectlAllowFail(
				"delete", "work",
				"-n", v1alpha1.SystemNamespace,
				"-l", selector,
				"--ignore-not-found=true",
			)

			// Wait until old Crossplane WorkPlacements are gone.
			Eventually(func() string {
				return strings.TrimSpace(platform.Kubectl(
					"get", "workplacement",
					"-n", v1alpha1.SystemNamespace,
					"-l", selector,
					"-o=jsonpath={.items[*].metadata.name}",
				))
			}).Should(BeEmpty(), "Expected no WorkPlacements for Crossplane Promise %s", crossplanePromiseName)

			// Wait until old Crossplane Works are gone.
			Eventually(func() string {
				return strings.TrimSpace(platform.Kubectl(
					"get", "work",
					"-n", v1alpha1.SystemNamespace,
					"-l", selector,
					"-o=jsonpath={.items[*].metadata.name}",
				))
			}).Should(BeEmpty(), "Expected no Works for Crossplane Promise %s", crossplanePromiseName)

			// Remove Crossplane's required label so Work scheduling stays pending for the pre-label assertions.
			By("ensuring destination does not have required Crossplane label")
			platform.KubectlAllowFail(
				"label", "destination", worker.Name,
				crossplaneLabelUnset,
			)

			// Apply the Crossplane Promise and defer its cleanup.
			By("applying the Crossplane Promise")
			platform.Kubectl("apply", "-f", crossplanePromiseURL)
			DeferCleanup(func() {
				platform.EventuallyKubectlDelete("promise", crossplanePromiseName)
				platform.KubectlAllowFail(
					"label", "destination", worker.Name,
					crossplaneLabelUnset,
				)
				platform.KubectlAllowFail(
					"delete", "work",
					"-n", v1alpha1.SystemNamespace,
					"-l", selector,
					"--ignore-not-found=true",
				)
			})

			// Check that Promise status is Available; this confirms setup completed even if Work scheduling is still
			// pending.
			By("waiting for Crossplane Promise to become available and create a Work")
			Eventually(func() string {
				return strings.TrimSpace(platform.Kubectl(
					"get", "promise", crossplanePromiseName,
					"-o=jsonpath={.status.status}",
				))
			}).Should(Equal(promiseStatusAvailable))

			// Check the first selected Work has expected identity fields:
			// 1. name follows Crossplane naming (<promise-name>-...),
			// 2. label kratix.io/promise-name points to this Promise.
			Eventually(func(g Gomega) {
				// Fetch the Work and output its name and its "kratix.io/promise-name" label's value.
				workFields := strings.TrimSpace(platform.Kubectl(
					"get", "work",
					"-n", v1alpha1.SystemNamespace,
					"-l", selector,
					fmt.Sprintf(
						"-o=jsonpath={.items[0].metadata.name}{\"%s\"}"+
							"{.items[0].metadata.labels.kratix\\.io/promise-name}",
						fieldDelimiter,
					),
				))

				// Split the packed output into work name and promise-name label, respectively.
				fields := strings.SplitN(workFields, fieldDelimiter, 2)
				g.Expect(fields).To(HaveLen(2))
				workName := fields[0]
				workPromiseNameLabelValue := fields[1]

				// Verify the Work name is for this Promise (name starts with "crossplane-").
				g.Expect(workName).To(MatchRegexp("^" + crossplanePromiseName + "-"))
				// Verify the Work's promise-name label points to this exact Promise.
				g.Expect(workPromiseNameLabelValue).To(Equal(crossplanePromiseName))
			}).Should(Succeed())

			// Fetch the Crossplane Work name for building the WorkPlacement label selector.
			workName := strings.TrimSpace(platform.Kubectl(
				"get", "work",
				"-n", v1alpha1.SystemNamespace,
				"-l", selector,
				"-o=jsonpath={.items[0].metadata.name}",
			))
			Expect(workName).NotTo(BeEmpty(), "Expected a Work resource for Crossplane Promise %s",
				crossplanePromiseName)

			// Build WorkPlacement selector in format: "kratix.io/work=<work-name>".
			// Crossplane WorkPlacements are labelled by Work name, not Promise name.
			// This selector is used for both WorkPlacement cleanup and final WorkPlacement assertion.
			workPlacementSelector := fmt.Sprintf("%s=%s", workLabelKey, workName)

			// Defer the cleanup of WorkPlacements created for this Crossplane Work.
			DeferCleanup(func() {
				platform.KubectlAllowFail(
					"delete", "workplacement",
					"-n", v1alpha1.SystemNamespace,
					"-l", workPlacementSelector,
					"--ignore-not-found=true",
				)
			})

			// Check Reconciled condition fields while Work cannot schedule:
			// 1. reason is "WorkflowPending",
			// 2. status is "Unknown".
			By("verifying pending Crossplane Promise conditions before label is added")
			Eventually(func(g Gomega) {
				// Fetch the Promise and output its Reconciled condition's reason and status.
				reconciledFields := strings.TrimSpace(platform.Kubectl(
					"get", "promise", crossplanePromiseName,
					fmt.Sprintf(
						"-o=jsonpath={.status.conditions[?(@.type==\"%s\")].reason}"+
							"{\"%s\"}{.status.conditions[?(@.type==\"%s\")].status}",
						conditionTypeReconciled,
						fieldDelimiter,
						conditionTypeReconciled,
					),
				))

				// Split the packed output into Reconciled condition's reason and status, respectively.
				fields := strings.SplitN(reconciledFields, fieldDelimiter, 2)
				g.Expect(fields).To(HaveLen(2))
				reconciledConditionReason := fields[0]
				reconciledConditionStatus := fields[1]

				// Verify the Reconciled condition's reason is "WorkflowPending"
				g.Expect(reconciledConditionReason).To(Equal(conditionReasonWorkflowPending))
				// Verify the Reconciled condition's status is "Unknown"
				g.Expect(reconciledConditionStatus).To(Equal(conditionStatusUnknown))
			}).Should(Succeed())

			// Check WorksSucceeded condition fields while Work cannot schedule:
			// 1. reason is "WorksPending",
			// 2. status is "Unknown".
			Eventually(func(g Gomega) {
				// Fetch the Promise and output its WorksSucceeded condition's reason and status.
				worksSucceededFields := strings.TrimSpace(platform.Kubectl(
					"get", "promise", crossplanePromiseName,
					fmt.Sprintf(
						"-o=jsonpath={.status.conditions[?(@.type==\"%s\")].reason}"+
							"{\"%s\"}{.status.conditions[?(@.type==\"%s\")].status}",
						conditionTypeWorksSucceeded,
						fieldDelimiter,
						conditionTypeWorksSucceeded,
					),
				))

				// Split the packed output into WorksSucceeded condition's reason and status, respectively.
				fields := strings.SplitN(worksSucceededFields, fieldDelimiter, 2)
				g.Expect(fields).To(HaveLen(2))
				worksSucceededConditionReason := fields[0]
				worksSucceededConditionStatus := fields[1]

				// Verify the WorksSucceeded condition's reason is "WorksPending"
				g.Expect(worksSucceededConditionReason).To(Equal(conditionReasonWorksPending))
				// Verify the WorksSucceeded condition's status is "Unknown"
				g.Expect(worksSucceededConditionStatus).To(Equal(conditionStatusUnknown))
			}).Should(Succeed())

			// Add "crossplane=enabled" label so the Crossplane Work can schedule.
			By("adding required Crossplane label to the worker destination")
			platform.Kubectl(
				"label", "destination", worker.Name,
				crossplaneLabelSet,
				"--overwrite",
			)

			// Check the first selected WorkPlacement name contains the Promise name and points to this exact
			// Work via label kratix.io/work=<work-name>.
			By("verifying Promise conditions transition to reconciled and works succeeded")
			Eventually(func(g Gomega) {
				// Fetch the WorkPlacement's name and its "kratix.io/work" label's value for the selected Work.
				workPlacementFields := strings.TrimSpace(platform.Kubectl(
					"get", "workplacement",
					"-n", v1alpha1.SystemNamespace,
					"-l", workPlacementSelector,
					fmt.Sprintf(
						"-o=jsonpath={.items[0].metadata.name}{\"%s\"}{.items[0].metadata.labels.kratix\\.io/work}",
						fieldDelimiter,
					),
				))

				// Split the WorkPlacement name and "kratix.io/work" label's value into two fields using the
				// fieldDelimiter.
				fields := strings.SplitN(workPlacementFields, fieldDelimiter, 2)
				g.Expect(fields).To(HaveLen(2))
				workPlacementName := fields[0]
				workPlacementWorkLabel := fields[1]

				// The WorkPlacement name should contain the Promise name, which confirms that the WorkPlacement is for
				// the expected Promise and not a different one created by another Work.
				g.Expect(workPlacementName).To(ContainSubstring(crossplanePromiseName))

				// The WorkPlacement should be labelled with the correct Work name, which confirms that the
				// WorkPlacement is for the expected Work and not a different one created by another Promise.
				g.Expect(workPlacementWorkLabel).To(Equal(workName))
			}).Should(Succeed())

			// Check Reconciled condition fields after label addition:
			// 1. reason switches to "Reconciled",
			// 2. status switches to "True".
			Eventually(func(g Gomega) {
				// Fetch the Promise and output its Reconciled condition's reason and status.
				reconciledFields := strings.TrimSpace(platform.Kubectl(
					"get", "promise", crossplanePromiseName,
					fmt.Sprintf(
						"-o=jsonpath={.status.conditions[?(@.type==\"%s\")].reason}"+
							"{\"%s\"}{.status.conditions[?(@.type==\"%s\")].status}",
						conditionTypeReconciled,
						fieldDelimiter,
						conditionTypeReconciled,
					),
				))

				// Split the packed output into Reconciled condition's reason and status, respectively.
				fields := strings.SplitN(reconciledFields, fieldDelimiter, 2)
				g.Expect(fields).To(HaveLen(2))
				reconciledConditionReason := fields[0]
				reconciledConditionStatus := fields[1]

				// Verify the Reconciled condition's reason is "Reconciled"
				g.Expect(reconciledConditionReason).To(Equal(conditionReasonReconciled))
				// Verify the Reconciled condition's status is "True"
				g.Expect(reconciledConditionStatus).To(Equal(conditionStatusTrue))
			}, 2*time.Minute).Should(Succeed())

			// Check WorksSucceeded condition fields after label addition:
			// 1. reason switches to "WorksSucceeded",
			// 2. status switches to "True".
			Eventually(func(g Gomega) {
				// Fetch the Promise and output its WorksSucceeded condition's reason and status.
				worksSucceededFields := strings.TrimSpace(platform.Kubectl(
					"get", "promise", crossplanePromiseName,
					fmt.Sprintf(
						"-o=jsonpath={.status.conditions[?(@.type==\"%s\")].reason}"+
							"{\"%s\"}{.status.conditions[?(@.type==\"%s\")].status}",
						conditionTypeWorksSucceeded,
						fieldDelimiter,
						conditionTypeWorksSucceeded,
					),
				))

				// Split the packed output into WorksSucceeded condition's reason and status, respectively.
				fields := strings.SplitN(worksSucceededFields, fieldDelimiter, 2)
				g.Expect(fields).To(HaveLen(2))
				worksSucceededConditionReason := fields[0]
				worksSucceededConditionStatus := fields[1]

				// Verify the WorksSucceeded condition's reason is "WorksSucceeded"
				g.Expect(worksSucceededConditionReason).To(Equal(conditionReasonWorksSucceeded))
				// Verify the WorksSucceeded condition's status is "True"
				g.Expect(worksSucceededConditionStatus).To(Equal(conditionStatusTrue))
			}, 2*time.Minute).Should(Succeed())
		})
	})
