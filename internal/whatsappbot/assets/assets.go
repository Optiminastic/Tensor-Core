// Package assets holds the images the shop sends to customers.
//
// Compiled into the binary rather than fetched from object storage, because
// the one image here is sent in the middle of a payment conversation: a
// customer who has just agreed to pay Rs 100 and gets nothing back is a
// customer who thinks the shop went quiet about money. An embedded file cannot
// be missing, cannot have been moved by somebody tidying a bucket, and needs
// no credentials to read.
package assets

import _ "embed"

// ReversePickupQR is the UPI QR the customer pays the Rs 100 reverse-pickup
// fee to: ALT OPTI MEDIA PRIVATE LIMITED, altopti@kotak.
//
// ONE FIXED QR FOR EVERY CUSTOMER, which is a real limitation and not an
// oversight. Nothing in the payment names the report it belongs to, so there
// is no automatic reconciliation: the screenshot the customer sends back is
// the only link, and a person has to look at it. That is exactly why the
// reverse flow has a payment_claimed state that only support can move to paid.
//
// A Razorpay payment link would carry a reference_id and settle this by
// webhook. It is the upgrade path, and it changes this file and nothing else
// about the flow.
//
//go:embed reverse-pickup-qr.jpg
var ReversePickupQR []byte

// ReversePickupQRType is what to serve it as.
const ReversePickupQRType = "image/jpeg"
