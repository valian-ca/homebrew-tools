class Frn < Formula
  desc "Fast Flutter run launcher with device picker"
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/frn-0.4.0.tar.gz"
  sha256 "15efb8efae0b5092bd697402d058a8b5cfe7960b82a40a79c242a5918e163c1e"
  license "MIT"

  depends_on "go" => :build

  def install
    cd "cmd/frn" do
      system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}")
    end
  end

  test do
    assert_match "frn", shell_output("#{bin}/frn --help")
    assert_match version.to_s, shell_output("#{bin}/frn --version")
  end
end
