provider "aws" {
  region  = "${var.region}"
  profile = "${var.profile}"
  version = "~> 1.32"
}

# Variables are not available in tf backend config blocks
terraform {
  backend "s3" {
    bucket         = "filecoin-terraform-state"
    key            = "filecoin-eks-us-east-1.tfstate"
    dynamodb_table = "filecoin-eks-terraform-state"
    region         = "us-east-1"
    profile        = "filecoin"
  }
}
