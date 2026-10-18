package main

import (
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/text/encoding/charmap"
)

const ratesURL = "https://www.cbr.ru/scripts/XML_daily.asp"

const defaultCodes = "USD,EUR,CNY,KZT,AED,TRY,GEL,AMD,BYN,UZS,KGS,THB,VND"

type Valute struct {
	CharCode string `xml:"CharCode"`
	Nominal  string `xml:"Nominal"`
	Name     string `xml:"Name"`
	Value    string `xml:"Value"`
}

type ValCurs struct {
	Date    string   `xml:"Date,attr"`
	Valutes []Valute `xml:"Valute"`
}

type Rate struct {
	Code    string
	Name    string
	Nominal int
	Value   float64
	PerUnit float64
}

var ErrCurrencyNotFound = errors.New("валюта не найдена")

func main() {
	codes := flag.String("codes", defaultCodes, "коды валют через запятую, например USD,EUR,KZT")
	amount := flag.Int("amount", 10000, "сумма в рублях для пересчёта")
	flag.Parse()

	client := &http.Client{Timeout: 10 * time.Second}

	curs, err := fetchRates(client)
	if err != nil {
		log.Fatal(err)
	}

	rates, missing, err := selectRates(curs.Valutes, parseCodes(*codes))
	if err != nil {
		log.Fatal(err)
	}

	for _, code := range missing {
		fmt.Fprintf(os.Stderr, "предупреждение: %s нет в ответе ЦБ\n", code)
	}

	fmt.Print(formatTable(curs.Date, rates, *amount))
}

func fetchRates(client *http.Client) (ValCurs, error) {
	req, err := http.NewRequest(http.MethodGet, ratesURL, nil)
	if err != nil {
		return ValCurs{}, fmt.Errorf("создание запроса: %w", err)
	}
	req.Header.Set("User-Agent", "currex/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return ValCurs{}, fmt.Errorf("запрос к ЦБ: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return ValCurs{}, fmt.Errorf("ЦБ вернул статус %d: %s", resp.StatusCode, body)
	}

	var curs ValCurs
	decoder := xml.NewDecoder(resp.Body)
	decoder.CharsetReader = charsetReader

	if err := decoder.Decode(&curs); err != nil {
		return ValCurs{}, fmt.Errorf("парсинг XML: %w", err)
	}

	return curs, nil
}

func charsetReader(label string, input io.Reader) (io.Reader, error) {
	if strings.EqualFold(label, "windows-1251") {
		return charmap.Windows1251.NewDecoder().Reader(input), nil
	}
	return nil, fmt.Errorf("неподдерживаемая кодировка: %s", label)
}

func selectRates(valutes []Valute, codes []string) ([]Rate, []string, error) {
	rates := make([]Rate, 0, len(codes))
	var missing []string

	for _, code := range codes {
		v, err := findValute(valutes, code)
		if errors.Is(err, ErrCurrencyNotFound) {
			missing = append(missing, code)
			continue
		}
		if err != nil {
			return nil, nil, err
		}

		rate, err := parseRate(v)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", code, err)
		}
		rates = append(rates, rate)
	}

	return rates, missing, nil
}

func findValute(valutes []Valute, code string) (Valute, error) {
	for _, v := range valutes {
		if v.CharCode == code {
			return v, nil
		}
	}
	return Valute{}, fmt.Errorf("%s: %w", code, ErrCurrencyNotFound)
}

func parseRate(v Valute) (Rate, error) {
	value, err := parseFloat(v.Value)
	if err != nil {
		return Rate{}, fmt.Errorf("поле Value %q: %w", v.Value, err)
	}

	nominal, err := strconv.Atoi(strings.TrimSpace(v.Nominal))
	if err != nil {
		return Rate{}, fmt.Errorf("поле Nominal %q: %w", v.Nominal, err)
	}
	if nominal <= 0 {
		return Rate{}, fmt.Errorf("некорректный Nominal: %q", v.Nominal)
	}

	return Rate{
		Code:    v.CharCode,
		Name:    v.Name,
		Nominal: nominal,
		Value:   value,
		PerUnit: value / float64(nominal),
	}, nil
}

func parseFloat(s string) (float64, error) {
	s = strings.Replace(strings.TrimSpace(s), ",", ".", 1)
	return strconv.ParseFloat(s, 64)
}

func parseCodes(s string) []string {
	var codes []string
	for _, part := range strings.Split(s, ",") {
		code := strings.ToUpper(strings.TrimSpace(part))
		if code != "" {
			codes = append(codes, code)
		}
	}
	return codes
}

func formatTable(date string, rates []Rate, amount int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Курсы ЦБ РФ на %s\n\n", date)

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Код\tВалюта\tКурс\tЗа %s ₽\n", formatThousands(amount))

	for _, r := range rates {
		course := fmt.Sprintf("%d %s = %s ₽", r.Nominal, r.Code, formatDecimal(r.Value, 4))
		converted := formatMoney(float64(amount) / r.PerUnit)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s %s\n", r.Code, r.Name, course, converted, r.Code)
	}
	w.Flush()

	return b.String()
}

func formatMoney(v float64) string {
	if v >= 1000 {
		return formatThousands(int(math.Round(v)))
	}
	return formatDecimal(v, 2)
}

func formatDecimal(value float64, prec int) string {
	return strings.Replace(strconv.FormatFloat(value, 'f', prec, 64), ".", ",", 1)
}

func formatThousands(amount int) string {
	in := strconv.Itoa(amount)
	out := make([]byte, len(in)+(len(in)-1)/3)

	j := len(out) - 1
	count := 0
	for i := len(in) - 1; i >= 0; i-- {
		out[j] = in[i]
		j--
		count++
		if count == 3 && i > 0 {
			out[j] = ' '
			j--
			count = 0
		}
	}

	return string(out)
}
