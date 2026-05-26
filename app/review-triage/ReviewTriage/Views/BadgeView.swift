import SwiftUI

struct BadgeView: View {
    let action: Action?
    let suggestion: Action?

    var body: some View {
        HStack(spacing: 4) {
            Image(systemName: BadgeStyle.symbol(action: action))
                .font(.caption2)
            Text(BadgeStyle.text(action: action, suggestion: suggestion))
                .font(.caption2.weight(.semibold))
                .monospaced()
        }
        .foregroundStyle(.white)
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
        .background(
            BadgeStyle.color(action: action),
            in: RoundedRectangle(cornerRadius: 4, style: .continuous)
        )
    }
}
