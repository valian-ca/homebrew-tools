import Foundation
import Testing
@testable import ReviewTriage

@Suite("BadgeStyle")
struct BadgeStyleTests {
    @Test func textWithFixActionReturnsFixRegardlessOfSuggestion() {
        #expect(BadgeStyle.text(action: .fix, suggestion: .fix) == "FIX")
        #expect(BadgeStyle.text(action: .fix, suggestion: .skip) == "FIX")
        #expect(BadgeStyle.text(action: .fix, suggestion: .discuss) == "FIX")
        #expect(BadgeStyle.text(action: .fix, suggestion: nil) == "FIX")
    }

    @Test func textWithSkipActionReturnsSkipRegardlessOfSuggestion() {
        #expect(BadgeStyle.text(action: .skip, suggestion: .fix) == "SKIP")
        #expect(BadgeStyle.text(action: .skip, suggestion: .skip) == "SKIP")
        #expect(BadgeStyle.text(action: .skip, suggestion: .discuss) == "SKIP")
        #expect(BadgeStyle.text(action: .skip, suggestion: nil) == "SKIP")
    }

    @Test func textWithDiscussActionReturnsDiscussRegardlessOfSuggestion() {
        #expect(BadgeStyle.text(action: .discuss, suggestion: .fix) == "DISCUSS")
        #expect(BadgeStyle.text(action: .discuss, suggestion: .skip) == "DISCUSS")
        #expect(BadgeStyle.text(action: .discuss, suggestion: .discuss) == "DISCUSS")
        #expect(BadgeStyle.text(action: .discuss, suggestion: nil) == "DISCUSS")
    }

    @Test func textWithNilActionReturnsSuggestionWithQuestionMark() {
        #expect(BadgeStyle.text(action: nil, suggestion: .fix) == "FIX?")
        #expect(BadgeStyle.text(action: nil, suggestion: .skip) == "SKIP?")
        #expect(BadgeStyle.text(action: nil, suggestion: .discuss) == "DISCUSS?")
    }

    @Test func textWithNilActionAndNilSuggestionReturnsQuestionMark() {
        #expect(BadgeStyle.text(action: nil, suggestion: nil) == "?")
    }

    @Test func symbolForFixActionReturnsCheckmarkCircle() {
        #expect(BadgeStyle.symbol(action: .fix) == "checkmark.circle.fill")
    }

    @Test func symbolForSkipActionReturnsMinusCircle() {
        #expect(BadgeStyle.symbol(action: .skip) == "minus.circle.fill")
    }

    @Test func symbolForDiscussActionReturnsBubbles() {
        #expect(BadgeStyle.symbol(action: .discuss) == "bubble.left.and.bubble.right.fill")
    }

    @Test func symbolForNilActionReturnsQuestionmarkCircle() {
        #expect(BadgeStyle.symbol(action: nil) == "questionmark.circle.fill")
    }

    @Test func colorDistinguishesAllActionStates() {
        let fixColor = BadgeStyle.color(action: .fix)
        let skipColor = BadgeStyle.color(action: .skip)
        let discussColor = BadgeStyle.color(action: .discuss)
        let nilColor = BadgeStyle.color(action: nil)

        #expect(fixColor != skipColor)
        #expect(fixColor != discussColor)
        #expect(fixColor != nilColor)
        #expect(skipColor != discussColor)
        #expect(skipColor != nilColor)
        #expect(discussColor != nilColor)
    }
}
