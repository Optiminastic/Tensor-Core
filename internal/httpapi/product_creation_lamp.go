package httpapi

// The moon night lamp (3DMS): the customer's photo and words on a moon,
// printed as a lithophane sphere - see internal/moonlamp. The order carries
// the photo as file-1 and the words as text-moon.

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/moonlamp"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// maxLampText bounds the words: the tool squeezes long lines, but a
// paragraph squeezed into 85 degrees is unreadable on a 65 mm ball.
const maxLampText = 40

func (s *Server) nightLamp(c *gin.Context) {
	order, format, ok := s.productCreationOrder(c, []string{"png", "3mf"})
	if !ok {
		return
	}
	photoURL, text, problem := nightLampFields(order)
	if problem != "" {
		detail(c, http.StatusUnprocessableEntity, problem)
		return
	}
	photo, err := fetchPhoto(c.Request.Context(), photoURL)
	if err != nil {
		detail(c, http.StatusBadGateway, fmt.Sprintf("Could not load the photo: %v", err))
		return
	}
	lamp, err := moonlamp.Build(photo, text, "")
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not compose the moon picture.")
		return
	}
	var out []byte
	contentType := "image/png"
	if format == "3mf" {
		out, err = meshio.Write3MF([]meshio.Part{{
			Name: "Moon lamp " + order.OrderNumber, Colour: "#FFFFFF", Material: "PLA", Triangles: lamp.Solid(),
		}})
		contentType = "model/3mf"
	} else {
		out, err = lamp.Preview()
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not build the night lamp.")
		return
	}
	name := fmt.Sprintf("%s_moon_lamp.%s", order.OrderNumber, format)
	if format == "png" {
		name = fmt.Sprintf("%s_moon_lamp.preview.png", order.OrderNumber)
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
	c.Data(http.StatusOK, contentType, out)
}

// nightLampFields finds the photo and the words on the order's lamp line.
//
// An order can carry other products beside the lamp - T3DPS-116420 has a
// photo plank first, with its own "Photo" field - so the lamp's own line is
// preferred: the one with file-1 / text-moon, or whose SKU or name says moon
// or lamp. Only with no such line is any line with a photo taken.
func nightLampFields(order gen.Order) (photo, text, problem string) {
	var lines []production.LineItem
	if err := jsonUnmarshalLines(order.LineItems, &lines); err != nil {
		return "", "", "This order's line items could not be read."
	}
	bestScore := -1
	sized := ""
	for _, li := range lines {
		// A 3DMS01-6P is the 6 inch moon, a different product with its own
		// option on the page - never built as the small lamp by mistake.
		if size, ok := moonlamp.SizeForSKU(deref(li.SKU)); ok {
			sized = fmt.Sprintf("%d inch", size.Inches)
			continue
		}
		p, t, score := lampLine(li)
		if p != "" && score > bestScore {
			photo, text, bestScore = p, t, score
		}
	}
	if photo == "" && sized != "" {
		return "", "", fmt.Sprintf("Order %s is a 3D Moon Lamp (%s), not the night lamp - choose 3D Moon Lamp.", order.OrderNumber, sized)
	}
	if photo == "" {
		return "", "", fmt.Sprintf("Order %s has no photo (file-1) - is it a night lamp order?", order.OrderNumber)
	}
	if len([]rune(text)) > maxLampText {
		return "", "", fmt.Sprintf("The lamp's text is %d characters; at most %d fit on the moon.", len([]rune(text)), maxLampText)
	}
	return photo, text, ""
}

// lampLine reads one line's photo and words, scoring how surely it is the lamp.
func lampLine(li production.LineItem) (photo, text string, score int) {
	what := strings.ToLower(deref(li.SKU) + " " + li.ProductName)
	if strings.Contains(what, "3dms") || strings.Contains(what, "moon") || strings.Contains(what, "lamp") {
		score += 2
	}
	for _, prop := range li.Properties {
		key := normalisePropKey(prop.Name)
		value := strings.TrimSpace(prop.Value)
		if strings.HasPrefix(prop.Name, "_") || value == "" {
			continue
		}
		switch {
		case key == "file 1" && strings.HasPrefix(value, "https://"):
			photo, score = value, score+2
		case photo == "" && (strings.Contains(key, "photo") || strings.Contains(key, "image")) &&
			strings.HasPrefix(value, "https://"):
			photo = value
		case key == "text moon":
			text, score = value, score+2
		case text == "" && strings.Contains(key, "text"):
			text = value
		}
	}
	return photo, text, score
}
