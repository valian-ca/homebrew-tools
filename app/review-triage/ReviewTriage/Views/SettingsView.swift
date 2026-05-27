import SwiftUI

struct SettingsView: View {
    @AppStorage(AppSettingsKeys.textFontSize) private var textSize: Double = AppSettingsDefaults.textFontSize
    @AppStorage(AppSettingsKeys.codeFontSize) private var codeSize: Double = AppSettingsDefaults.codeFontSize
    @AppStorage(AppSettingsKeys.textFontFamily) private var textFamily: String = AppSettingsDefaults.textFontFamily
    @AppStorage(AppSettingsKeys.codeFontFamily) private var codeFamily: String = AppSettingsDefaults.codeFontFamily

    var body: some View {
        TabView {
            fontsTab
                .tabItem { Label("Fonts", systemImage: "textformat") }
        }
        .frame(width: 520, height: 460)
    }

    private var fontsTab: some View {
        Form {
            Section("Text") {
                familyPicker(selection: $textFamily, families: FontCatalog.textFamilies, systemLabel: "System (SF Pro)")
                sizeRow(value: $textSize)
                previewRow(font: previewPalette.body, sample: "The quick brown fox jumps over the lazy dog.")
            }
            Section("Code") {
                familyPicker(selection: $codeFamily, families: FontCatalog.monospacedFamilies, systemLabel: "System Monospaced (SF Mono)")
                sizeRow(value: $codeSize)
                previewRow(font: previewPalette.code, sample: "if (!user.id) return res.status(400);")
            }
            Section {
                Button("Reset to defaults") { resetToDefaults() }
            }
        }
        .formStyle(.grouped)
        .padding(.bottom, 8)
    }

    private func familyPicker(selection: Binding<String>, families: [String], systemLabel: String) -> some View {
        Picker("Family", selection: selection) {
            Text(systemLabel).tag("")
            Divider()
            ForEach(families, id: \.self) { family in
                Text(family).tag(family)
            }
        }
    }

    private func sizeRow(value: Binding<Double>) -> some View {
        HStack {
            Text("Size")
            Slider(
                value: value,
                in: AppSettingsDefaults.minFontSize...AppSettingsDefaults.maxFontSize,
                step: 1
            ) { Text("Size") }
            Text("\(Int(value.wrappedValue)) pt")
                .monospacedDigit()
                .frame(width: 50, alignment: .trailing)
                .foregroundStyle(.secondary)
        }
    }

    private func previewRow(font: Font, sample: String) -> some View {
        HStack {
            Text("Preview")
                .foregroundStyle(.secondary)
            Text(sample)
                .font(font)
                .lineLimit(1)
                .truncationMode(.tail)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private var previewPalette: FontPalette {
        FontPalette(textSize: textSize, codeSize: codeSize, textFamily: textFamily, codeFamily: codeFamily)
    }

    private func resetToDefaults() {
        textSize = AppSettingsDefaults.textFontSize
        codeSize = AppSettingsDefaults.codeFontSize
        textFamily = AppSettingsDefaults.textFontFamily
        codeFamily = AppSettingsDefaults.codeFontFamily
    }
}
