package statepool

import (
	"encoding/json"
	"strings"
)

const candyProblem = `A bag contains candies:
                 apple peach watermelon
round                7     9          8
five-pointed star    7     6          4
You can identify and CHOOSE shape by touch but cannot identify flavor.
Draw without replacement. Before drawing, fix how many round candies and
how many star candies to take. Guarantee at least one of these pairs:
(round apple AND star peach) OR (round peach AND star apple).
Minimize the total drawn. Give integer fields total, round_count, star_count,
and string fields guarantee_reason and minimality_reason explaining your proof.`

const CapturePrompt = candyProblem + ` Return only one JSON object with those five fields, without Markdown.`

const VerifyPrompt = candyProblem + `
Also solve these independent questions exactly:
a: Count length-12 binary strings with exactly five 1s and no adjacent 1s.
b: Find the smallest positive n with n mod 7=3, n mod 9=4, n mod 11=5.
c: Count permutations of 1,2,3,4,5,6,7,8 with no element in its original position.
Return one valid JSON object using this exact structure. Replace the zero values
with your answers and the empty strings with your proofs:
{"candy":{"total":0,"round_count":0,"star_count":0,"guarantee_reason":"","minimality_reason":""},"checks":{"a":0,"b":0,"c":0}}
The candy and checks objects must be siblings. Close the candy object before
writing checks, and close the root object at the end. Do not include Markdown.`

type candyAnswer struct {
	Total      *int   `json:"total"`
	Round      *int   `json:"round_count"`
	Star       *int   `json:"star_count"`
	Guarantee  string `json:"guarantee_reason"`
	Minimality string `json:"minimality_reason"`
}

// Exhaustive witnesses validate the allocation and optimum independently of prose.
func candyAllocationValid(round, star int) bool {
	if round < 0 || round > 24 || star < 0 || star > 17 {
		return false
	}
	for ra := 0; ra <= 7; ra++ {
		for rp := 0; rp <= 9; rp++ {
			rw := round - ra - rp
			if rw < 0 || rw > 8 {
				continue
			}
			for sa := 0; sa <= 7; sa++ {
				for sp := 0; sp <= 6; sp++ {
					sw := star - sa - sp
					if sw >= 0 && sw <= 4 && !(ra > 0 && sp > 0 || rp > 0 && sa > 0) {
						return false
					}
				}
			}
		}
	}
	return true
}

func gradeCandy(answer candyAnswer) bool {
	if answer.Total == nil || answer.Round == nil || answer.Star == nil || *answer.Total < 0 || *answer.Total > 41 ||
		*answer.Total != *answer.Round+*answer.Star || answer.Guarantee == "" || answer.Minimality == "" ||
		!candyAllocationValid(*answer.Round, *answer.Star) {
		return false
	}
	for r := 0; r <= 24; r++ {
		for s := 0; s <= 17; s++ {
			if r+s < *answer.Total && candyAllocationValid(r, s) {
				return false
			}
		}
	}
	return true
}

func Grade(text string, verification bool) bool {
	return GradeFailure(text, verification) == ""
}

func GradeFailure(text string, verification bool) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		if index := strings.IndexByte(text, '\n'); index >= 0 {
			text = strings.TrimSuffix(strings.TrimSpace(text[index+1:]), "```")
		}
	}
	if !verification {
		var answer candyAnswer
		if json.Unmarshal([]byte(text), &answer) != nil || answer.Total == nil || answer.Round == nil || answer.Star == nil {
			return "invalid_answer_format"
		}
		if !gradeCandy(answer) {
			return "benchmark_failed"
		}
		return ""
	}
	var answer struct {
		Candy  candyAnswer    `json:"candy"`
		Checks map[string]int `json:"checks"`
	}
	if json.Unmarshal([]byte(text), &answer) != nil || answer.Candy.Total == nil || answer.Candy.Round == nil || answer.Candy.Star == nil {
		return "invalid_answer_format"
	}
	for _, name := range []string{"a", "b", "c"} {
		if _, found := answer.Checks[name]; !found {
			return "invalid_answer_format"
		}
	}
	if !gradeCandy(answer.Candy) || answer.Checks["a"] != 56 || answer.Checks["b"] != 346 || answer.Checks["c"] != 14833 {
		return "benchmark_failed"
	}
	return ""
}
