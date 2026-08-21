package httpapi

import (
	"bytes"
	"context"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

// downloadPersonalisedModel streams the baked (name-merged) STL a design carries
// once a personalisation has been saved. The bake stores it at personalised_stl_key;
// this hands those exact bytes back. When no personalisation is applied the key is
// NULL and there is nothing to download.
func (s *Server) downloadPersonalisedModel(c *gin.Context) {
	d, ok := s.designForDownload(c)
	if !ok {
		return
	}
	if d.PersonalisedStlKey == nil || *d.PersonalisedStlKey == "" {
		detail(c, http.StatusNotFound, "No personalised model yet. Apply and save a name first.")
		return
	}
	key := *d.PersonalisedStlKey
	filename := safeName(d.Name) + "-personalised" + strings.ToLower(filepath.Ext(key))
	s.streamObject(c, key, filename)
}

// downloadOrientedModel streams the base STL rotated to the least-support resting
// pose the recommender computes ("best angle for less support"). The rotated mesh
// is built once and cached in object storage; an already-optimal model (or any
// failure) falls back to the plain base STL. Only STL bases are supported.
func (s *Server) downloadOrientedModel(c *gin.Context) {
	d, ok := s.designForDownload(c)
	if !ok {
		return
	}
	if d.StlKey == "" {
		detail(c, http.StatusNotFound, "No model file is available for this design.")
		return
	}
	name := safeName(d.Name)
	if strings.ToLower(filepath.Ext(d.StlKey)) != ".stl" {
		// Only STL can be re-oriented; other formats stream as-is.
		s.streamObject(c, d.StlKey, name+strings.ToLower(filepath.Ext(d.StlKey)))
		return
	}
	orientedKey := d.StlKey + ".oriented.stl"
	if err := s.ensureOrientedModel(c.Request.Context(), d.StlKey, orientedKey); err != nil {
		// Any failure: fall back to the plain model so the download still works.
		s.streamObject(c, d.StlKey, name+".stl")
		return
	}
	s.streamObject(c, orientedKey, name+"-oriented.stl")
}

// ensureOrientedModel builds the least-support orientation of the base STL once and
// caches it under orientedKey, returning nil once it exists. An already-optimal
// model is stored as-is (only re-encoded), so the download always succeeds.
func (s *Server) ensureOrientedModel(ctx context.Context, srcKey, orientedKey string) error {
	if obj, err := s.storage.Get(ctx, orientedKey); err == nil {
		_ = obj.Body.Close()
		return nil
	} else if !storage.IsNotFound(err) {
		return err
	}

	src, err := s.storage.Get(ctx, srcKey)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(src.Body)
	_ = src.Body.Close()
	if err != nil {
		return err
	}
	mesh, err := orientation.LoadSTL(data)
	if err != nil {
		return err
	}

	tris := mesh.Triangles
	if rec, ok := orientation.Recommend(mesh, orientation.Options{}); ok &&
		!rec.AlreadyOptimal && rec.RotationDegrees != 0 {
		ax, ay, az := rodriguesBasis(rec.RotationAxis, rec.RotationDegrees)
		tris = meshio.OrientTriangles(mesh.Triangles, ax, ay, az)
	}

	// MergeBinarySTL normalises the (rotated) part so its minimum corner sits at the
	// origin - i.e. it rests flat on the bed in the recommended pose.
	stl := meshio.MergeBinarySTL("tensor-oriented", []meshio.Placed{{Triangles: tris}})
	return s.storage.Put(ctx, orientedKey, bytes.NewReader(stl), int64(len(stl)), "model/stl")
}

// rodriguesBasis returns the columns of the rotation matrix for a right-handed
// rotation of degrees about the given axis (Rodrigues' formula), as the three world
// axes that local +X/+Y/+Z map onto - exactly what meshio.OrientTriangles expects.
// A degenerate axis yields the identity basis (no rotation).
func rodriguesBasis(axis orientation.Vec3, degrees float64) (ax, ay, az orientation.Vec3) {
	length := math.Sqrt(axis.X*axis.X + axis.Y*axis.Y + axis.Z*axis.Z)
	if length < 1e-9 {
		return orientation.Vec3{X: 1}, orientation.Vec3{Y: 1}, orientation.Vec3{Z: 1}
	}
	kx, ky, kz := axis.X/length, axis.Y/length, axis.Z/length
	rad := degrees * math.Pi / 180
	c, s := math.Cos(rad), math.Sin(rad)
	t := 1 - c
	// R columns (world = R * local).
	ax = orientation.Vec3{X: c + kx*kx*t, Y: ky*kx*t + kz*s, Z: kz*kx*t - ky*s}
	ay = orientation.Vec3{X: kx*ky*t - kz*s, Y: c + ky*ky*t, Z: kz*ky*t + kx*s}
	az = orientation.Vec3{X: kx*kz*t + ky*s, Y: ky*kz*t - kx*s, Z: c + kz*kz*t}
	return ax, ay, az
}
