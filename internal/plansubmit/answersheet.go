package plansubmit

import (
	"context"
	"math/rand"
	"time"

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

type adminAnswerSheetSubmitClient interface {
	SubmitAnswerSheetAdminAttempt(context.Context, AdminSubmitAnswerSheetRequest, string, time.Duration) (*SubmitAnswerSheetResponse, error)
}

func buildAdminSubmitAnswerSheetRequest(req SubmitAnswerSheetRequest) AdminSubmitAnswerSheetRequest {
	return AdminSubmitAnswerSheetRequest{
		QuestionnaireCode:    req.QuestionnaireCode,
		QuestionnaireVersion: req.QuestionnaireVersion,
		IdempotencyKey:       req.IdempotencyKey,
		Title:                req.Title,
		TesteeID:             req.TesteeID,
		TaskID:               req.TaskID,
		Answers:              req.Answers,
	}
}

func logBuiltAnswers(logger interface{ Infow(string, ...interface{}) }, answers []Answer, questionnaireCode, testeeID string) {
	questionTypes := make([]string, 0, len(answers))
	for _, answer := range answers {
		questionTypes = append(questionTypes, answer.QuestionType)
	}

	logger.Infow("Built answers",
		"questionnaire_code", questionnaireCode,
		"testee_id", testeeID,
		"answer_count", len(answers),
		"question_types", questionTypes,
	)
}

func logSubmitRequest(logger interface{ Infow(string, ...interface{}) }, req SubmitAnswerSheetRequest, testeeIDStr string) {
	questionTypes := make([]string, 0, len(req.Answers))
	for _, answer := range req.Answers {
		questionTypes = append(questionTypes, answer.QuestionType)
	}

	logger.Infow("Submit answer sheet request",
		"testee_id", testeeIDStr,
		"testee_id_uint64", req.TesteeID,
		"questionnaire_code", req.QuestionnaireCode,
		"questionnaire_version", req.QuestionnaireVersion,
		"title", req.Title,
		"task_id", req.TaskID,
		"answer_count", len(req.Answers),
		"question_types", questionTypes,
	)
}

func validateAnswers(detail *QuestionnaireDetailResponse, answers []Answer) []map[string]interface{} {
	return toolanswersheet.Validate(toToolQuestionnaire(detail), toToolAnswers(answers))
}

func buildAnswers(detail *QuestionnaireDetailResponse, rng *rand.Rand) []Answer {
	return fromToolAnswers(toolanswersheet.BuildAnswers(toToolQuestionnaire(detail), rng))
}

func collectQuestionTypes(detail *QuestionnaireDetailResponse) []string {
	return toolanswersheet.CollectQuestionTypes(toToolQuestionnaire(detail))
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
