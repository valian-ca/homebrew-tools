class Lanes < Formula
  desc "Give each git worktree its own local stack, run by the lanesd daemon"
  homepage "https://github.com/valian-ca/homebrew-tools"
  url "https://github.com/valian-ca/homebrew-tools/archive/refs/tags/lanes-0.1.0.tar.gz"
  sha256 "0000000000000000000000000000000000000000000000000000000000000000"
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
