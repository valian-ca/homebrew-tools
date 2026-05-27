cask "review-triage" do
  version "0.3.0"
  sha256 "e5c5b15b6dec0e5d7b7fae4f4b32feca58e7c9bd7f0407439e10047693eb82c5"

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
