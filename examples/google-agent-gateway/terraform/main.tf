# Atryum x Google Agent Gateway authorization extension.
#
# Provisions the Atryum ext_authz callout as an Agent Gateway authorization
# extension. Resources 1-4 (Cloud Run -> NEG -> backend service -> authz
# extension) are VERIFIED working (see ../RESULTS.md). Resources 5-6 (the managed
# Agent Gateway and the authz-policy bind) are gated on the managed gateway being
# provisionable in your project; they are included, commented, for when it is.
#
# Usage:
#   terraform init && terraform apply \
#     -var project=singular-range-486318-b0 -var region=us-central1 \
#     -var image=us-central1-docker.pkg.dev/PROJECT/atryum/callout:v1

terraform {
  required_providers {
    google      = { source = "hashicorp/google", version = ">= 5.40" }
    google-beta = { source = "hashicorp/google-beta", version = ">= 5.40" }
  }
}

variable "project" { type = string }
variable "region" { type = string, default = "us-central1" }
variable "image" { type = string, description = "Atryum callout container image" }
variable "callout_timeout" { type = string, default = "10s", description = "Max 10s (Agent Gateway hard ceiling)" }

provider "google" {
  project = var.project
  region  = var.region
}

# 1) Cloud Run callout (gRPC over h2c; Cloud Run terminates TLS).
resource "google_cloud_run_v2_service" "callout" {
  name     = "atryum-callout"
  location = var.region
  ingress  = "INGRESS_TRAFFIC_ALL"

  template {
    containers {
      image = var.image
      ports {
        name           = "h2c" # HTTP/2 cleartext to the container for gRPC
        container_port = 8080
      }
      resources {
        limits = { cpu = "1", memory = "512Mi" }
      }
    }
    scaling {
      min_instance_count = 0
      max_instance_count = 2
    }
  }
}

# 2) Serverless NEG -> Cloud Run.
resource "google_compute_region_network_endpoint_group" "neg" {
  name                  = "atryum-callout-neg"
  region                = var.region
  network_endpoint_type = "SERVERLESS"
  cloud_run {
    service = google_cloud_run_v2_service.callout.name
  }
}

# 3) Regional internal backend service (HTTP/2 for gRPC).
resource "google_compute_region_backend_service" "be" {
  name                  = "atryum-callout-be"
  region                = var.region
  load_balancing_scheme = "INTERNAL_MANAGED"
  protocol              = "HTTP2"
  backend {
    group           = google_compute_region_network_endpoint_group.neg.id
    balancing_mode  = "UTILIZATION"
    capacity_scaler = 1.0
  }
}

# 4) Authorization extension: the Atryum ext_authz callout. VERIFIED.
resource "google_network_services_authz_extension" "atryum" {
  provider              = google-beta
  name                  = "atryum-authz"
  location              = var.region
  load_balancing_scheme = "INTERNAL_MANAGED"
  authority             = "atryum.authz"
  service               = google_compute_region_backend_service.be.self_link
  wire_format           = "EXT_AUTHZ_GRPC"
  fail_open             = false # fail CLOSED: deny on timeout/error
  timeout               = var.callout_timeout
}

# 5) The managed Agent Gateway (MCP, Google-managed). NOTE: in a fresh trial
#    project this returned a Google-side internal error (see ../RESULTS.md). Enable
#    when the managed gateway provisions in your project, or attach a self-managed
#    ALB / Secure Web Proxy instead.
#
# resource "google_network_services_agent_gateway" "gw" {
#   provider  = google-beta
#   name      = "atryum-gw"
#   location  = var.region
#   protocols = ["MCP"]
#   google_managed {}
# }

# 6) Bind the authz extension to the gateway for EVERY routed agent's tool call.
#    action=CUSTOM -> custom_provider.authz_extension, target = the gateway.
#
# resource "google_network_security_authz_policy" "bind" {
#   provider = google-beta
#   name     = "atryum-policy"
#   location = var.region
#   target {
#     load_balancing_scheme = "INTERNAL_MANAGED"
#     resources             = [google_network_services_agent_gateway.gw.id]
#   }
#   action = "CUSTOM"
#   custom_provider {
#     authz_extension {
#       resources = [google_network_services_authz_extension.atryum.id]
#     }
#   }
# }

output "callout_url" { value = google_cloud_run_v2_service.callout.uri }
output "authz_extension" { value = google_network_services_authz_extension.atryum.id }
