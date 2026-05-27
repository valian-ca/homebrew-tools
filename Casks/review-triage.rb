cask "review-triage" do
  version "0.2.0"
  sha256 "0d8d7ae6c947647167f695cbba9f7be970f03d89b83f28286d9695113bb86a21"

  url "https://github.com/valian-ca/homebrew-tools/releases/download/review-triage-app-#{version}/ReviewTriage-#{version}.zip"
  name "Review Triage"
  desc "Native macOS app for triaging code-review findings"
  homepage "https://github.com/valian-ca/homebrew-tools"

  depends_on macos: ">= :sequoia"

  app "ReviewTriage.app"
  binary "#{appdir}/ReviewTriage.app/Contents/MacOS/review-triage-cli", target: "review-triage"

  zap trash: [
    "~/Library/Application Support/ca.valian.review-triage",
    "~/Library/Preferences/ca.valian.review-triage.plist",
    "~/Library/Caches/ca.valian.review-triage",
  ]
end
