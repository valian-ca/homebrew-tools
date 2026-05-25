class ReviewTriage < Formula
  desc "Interactive TUI to triage code-review findings for valian:review"
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/review-triage-0.1.0.tar.gz"
  sha256 "0000000000000000000000000000000000000000000000000000000000000000"
  license "MIT"

  depends_on "go" => :build

  def install
    cd "cmd/review-triage" do
      system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}")
    end
  end

  test do
    assert_match "review-triage", shell_output("#{bin}/review-triage --help")
    assert_match version.to_s, shell_output("#{bin}/review-triage --version")
  end
end
