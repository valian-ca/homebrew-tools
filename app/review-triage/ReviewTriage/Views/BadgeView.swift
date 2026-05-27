import SwiftUI

struct BadgeView: View {
    @Environment(\.fontPalette) private var palette
    let action: Action?
    let suggestion: Action?

    var body: some View {
        HStack(spacing: 4) {
            Image(systemName: BadgeStyle.symbol(action: action))
                .font(palette.caption)
            Text(BadgeStyle.text(action: action, suggestion: suggestion))
                .font(palette.captionSemibold)
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
