class Frn < Formula
  desc "Fast Flutter run launcher with device picker"
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/frn-0.3.2.tar.gz"
  sha256 "984dfa6b18ad838549df152e1bdd616b11b234677dde9c790f1afbb3fb42f368"
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
