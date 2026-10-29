class Lanes < Formula
  desc "Give each git worktree its own local stack, run by the lanesd daemon"
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/lanes-0.2.0.tar.gz"
  sha256 "5e1bfa8e5fbc92e7df131987630092578b64c01d81ece95e0472b80402e903c5"
  license "MIT"
  head "https://github.com/valian-ca/homebrew-tools.git", branch: "main"

  depends_on "go" => :build
  depends_on "f1bonacc1/tap/process-compose"
  depends_on :macos

  def install
    ldflags = "-s -w -X main.version=#{version}"
    system "go", "build", *std_go_args(ldflags:), "./cmd/lanes"
    system "go", "build", *std_go_args(output: bin/"lanesd", ldflags:), "./cmd/lanesd"
  end

  service do
    run [opt_bin/"lanesd"]
    keep_alive successful_exit: false
    environment_variables PATH: std_service_path_env
    log_path var/"log/lanes.log"
    error_log_path var/"log/lanes.log"
  end

  test do
    assert_match "lanes", shell_output("#{bin}/lanes --help")
    assert_match version.to_s, shell_output("#{bin}/lanes --version")
    assert_match "lanesd", shell_output("#{bin}/lanesd --help")
    assert_match version.to_s, shell_output("#{bin}/lanesd --version")
  end
end
