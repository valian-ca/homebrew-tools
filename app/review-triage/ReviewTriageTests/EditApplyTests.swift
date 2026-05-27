import Foundation
import Testing
@testable import ReviewTriage

@Suite("EditApply")
struct EditApplyTests {
    @Test func singleEditReplacesAnchor() throws {
        let result = try EditApply.apply(
            edits: [Edit(find: "alpha", replace: "gamma")],
            to: "alpha\nbeta\n"
        )
        #expect(result == "gamma\nbeta\n")
    }

    @Test func multipleEditsApplyInOrder() throws {
        let result = try EditApply.apply(
            edits: [
                Edit(find: "alpha", replace: "ALPHA"),
                Edit(find: "beta", replace: "BETA"),
            ],
            to: "alpha\nbeta\ngamma\n"
        )
        #expect(result == "ALPHA\nBETA\ngamma\n")
    }

    @Test func laterEditCanAnchorOnEarlierReplacement() throws {
        // Sequencing is meaningful: edit #1 looks at the text *after* edit #0
        // has been applied. Here `BETA` doesn't exist until edit #0 runs.
        let result = try EditApply.apply(
            edits: [
                Edit(find: "beta", replace: "BETA"),
                Edit(find: "BETA", replace: "betaPrime"),
            ],
            to: "alpha\nbeta\n"
        )
        #expect(result == "alpha\nbetaPrime\n")
    }

    @Test func multilineFindAndReplace() throws {
        let source = """
        @Riverpod(keepAlive: true)
        AppContext appContext(Ref ref) {
          final equipments = ref.watch(equipmentsPreloaderProvider).value ?? const {};
        """
        let result = try EditApply.apply(
            edits: [Edit(
                find: "  final equipments = ref.watch(equipmentsPreloaderProvider).value ?? const {};",
                replace: """
                  final equipmentsAsync = ref.watch(equipmentsPreloaderProvider);
                  if (equipmentsAsync.hasError) {
                    logger.w('equipmentsPreloader failed', equipmentsAsync.error);
                  }
                  final equipments = equipmentsAsync.value ?? const {};
                """
            )],
            to: source
        )
        #expect(result.contains("equipmentsAsync.hasError"))
        #expect(!result.contains("ref.watch(equipmentsPreloaderProvider).value ?? const {};"))
    }

    @Test func emptyFindThrows() {
        #expect(throws: EditApplyError.findEmpty(editIndex: 0)) {
            try EditApply.apply(
                edits: [Edit(find: "", replace: "z")],
                to: "anything"
            )
        }
    }

    @Test func anchorNotFoundThrows() {
        #expect(throws: EditApplyError.notFound(editIndex: 0)) {
            try EditApply.apply(
                edits: [Edit(find: "missing", replace: "z")],
                to: "alpha\nbeta\n"
            )
        }
    }

    @Test func ambiguousAnchorThrows() {
        #expect(throws: EditApplyError.ambiguous(editIndex: 0, occurrences: 3)) {
            try EditApply.apply(
                edits: [Edit(find: "foo", replace: "BAR")],
                to: "foo foo foo"
            )
        }
    }

    @Test func errorEditIndexReflectsSequence() {
        // The third edit (index 2) is the broken one; the first two are valid.
        #expect(throws: EditApplyError.notFound(editIndex: 2)) {
            try EditApply.apply(
                edits: [
                    Edit(find: "a", replace: "A"),
                    Edit(find: "b", replace: "B"),
                    Edit(find: "missing", replace: "Z"),
                ],
                to: "a\nb\nc\n"
            )
        }
    }

    @Test func emptyEditsListReturnsSourceUnchanged() throws {
        let result = try EditApply.apply(edits: [], to: "alpha")
        #expect(result == "alpha")
    }
}
