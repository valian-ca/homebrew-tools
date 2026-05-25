class ReviewTriage < Formula
  desc "Interactive TUI to triage code-review findings for valian:review"
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/review-triage-0.1.0.tar.gz"
  sha256 "0f35686aac2278f3b760dc073f8bdad616d2949d6fe0e7d3b9a09953a311cd54"
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
