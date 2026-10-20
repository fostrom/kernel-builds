variable "VERSION" {}
variable "COMMIT" { default = "unknown" }
variable "SOURCE_DATE_EPOCH" { default = "0" }

group "default" {
  targets = ["amd64", "arm64", "rpi"]
}

target "kernel" {
  target = "release"
  output = ["type=local,dest=.release"]
  args = {
    VERSION           = VERSION
    COMMIT            = COMMIT
    SOURCE_DATE_EPOCH = SOURCE_DATE_EPOCH
  }
}

target "sources" {
  target = "sources"
  output = ["type=local,dest=.release"]
}

target "amd64" {
  inherits = ["kernel"]
  args = {
    TARGET = "amd64"
    SOURCE = "linux"
  }
}

target "arm64" {
  inherits = ["kernel"]
  args = {
    TARGET = "arm64"
    SOURCE = "linux"
  }
}

target "rpi" {
  inherits = ["kernel"]
  args = {
    TARGET = "rpi"
    SOURCE = "linux-rpi"
  }
}
