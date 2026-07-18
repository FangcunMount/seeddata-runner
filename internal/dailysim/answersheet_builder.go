package dailysim

import (
	"math/rand"

	toolanswersheet "github.com/FangcunMount/seeddata-runner/internal/answersheet"
)

const (
	questionTypeRadio    = toolanswersheet.QuestionTypeRadio
	questionTypeCheckbox = toolanswersheet.QuestionTypeCheckbox
	questionTypeText     = toolanswersheet.QuestionTypeText
	questionTypeTextarea = toolanswersheet.QuestionTypeTextarea
	questionTypeNumber   = toolanswersheet.QuestionTypeNumber
	questionTypeSection  = toolanswersheet.QuestionTypeSection
)

func validateAnswers(detail *QuestionnaireDetailResponse, answers []Answer) []map[string]interface{} {
	return toolanswersheet.Validate(toToolQuestionnaire(detail), toToolAnswers(answers))
}

func buildAnswers(detail *QuestionnaireDetailResponse, rng *rand.Rand) []Answer {
	return fromToolAnswers(toolanswersheet.BuildAnswers(toToolQuestionnaire(detail), rng))
}

func normalizeQuestionType(raw string) string {
	return toolanswersheet.NormalizeQuestionType(raw)
}

func collectQuestionTypes(detail *QuestionnaireDetailResponse) []string {
	return toolanswersheet.CollectQuestionTypes(toToolQuestionnaire(detail))
}

func resolveQuestionType(question QuestionResponse) string {
	return toolanswersheet.ResolveQuestionType(toToolQuestion(question))
}

func debugLogQuestionnaire(detail *QuestionnaireDetailResponse, logger interface{ Debugw(string, ...interface{}) }) {
	questionnaire := toToolQuestionnaire(detail)
	if len(questionnaire.Questions) == 0 {
		return
	}
	logger.Debugw("Questionnaire detail preview",
		"code", questionnaire.Code,
		"title", questionnaire.Title,
		"type", questionnaire.Type,
		"question_count", len(questionnaire.Questions),
		"questions", toolanswersheet.PreviewQuestionnaire(questionnaire, 3),
	)
}

func truncateString(value string, max int) string {
	return toolanswersheet.Truncate(value, max)
}

func toToolQuestionnaire(detail *QuestionnaireDetailResponse) toolanswersheet.Questionnaire {
	if detail == nil {
		return toolanswersheet.Questionnaire{}
	}
	questions := make([]toolanswersheet.Question, 0, len(detail.Questions))
	for _, question := range detail.Questions {
		questions = append(questions, toToolQuestion(question))
	}
	return toolanswersheet.Questionnaire{
		Code:      detail.Code,
		Title:     detail.Title,
		Version:   detail.Version,
		Type:      detail.Type,
		Questions: questions,
	}
}

func toToolQuestion(question QuestionResponse) toolanswersheet.Question {
	options := make([]toolanswersheet.Option, 0, len(question.Options))
	for _, option := range question.Options {
		options = append(options, toolanswersheet.Option{
			Code:    option.Code,
			Content: option.Content,
			Score:   option.Score,
		})
	}
	validationRules := make([]toolanswersheet.ValidationRule, 0, len(question.ValidationRules))
	for _, rule := range question.ValidationRules {
		validationRules = append(validationRules, toolanswersheet.ValidationRule{
			RuleType:    rule.RuleType,
			TargetValue: rule.TargetValue,
		})
	}
	return toolanswersheet.Question{
		Code:            question.Code,
		Type:            question.Type,
		Title:           question.Title,
		Options:         options,
		ValidationRules: validationRules,
	}
}

func toToolAnswers(answers []Answer) []toolanswersheet.Answer {
	out := make([]toolanswersheet.Answer, 0, len(answers))
	for _, answer := range answers {
		out = append(out, toolanswersheet.Answer{
			QuestionCode: answer.QuestionCode,
			QuestionType: answer.QuestionType,
			Score:        answer.Score,
			Value:        answer.Value,
		})
	}
	return out
}

func fromToolAnswers(answers []toolanswersheet.Answer) []Answer {
	out := make([]Answer, 0, len(answers))
	for _, answer := range answers {
		out = append(out, Answer{
			QuestionCode: answer.QuestionCode,
			QuestionType: answer.QuestionType,
			Score:        answer.Score,
			Value:        answer.Value,
		})
	}
	return out
}
