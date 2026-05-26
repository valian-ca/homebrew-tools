cask "review-triage" do
  version "0.1.0"
  sha256 "ca44a08ff4f2c2178793836682bf29be2de27f0439d05727bbdbe55ba3756269"

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
