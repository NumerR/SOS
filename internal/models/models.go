package models

// User — пользователь системы.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	FullName string `json:"full_name"`
	Role     string `json:"role"`
}

// UserWithSecret — пользователь вместе с хэшем пароля.
// Используется только на сервере при входе.
type UserWithSecret struct {
	User
	PasswordHash string
}

// Course — учебный курс.
type Course struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Teacher     string `json:"teacher"`
	CreatedBy   int64  `json:"created_by"`
}

// CreateCourseInput — данные для создания курса.
type CreateCourseInput struct {
	Title       string
	Description string
	Teacher     string
}

// Assignment — учебное задание на курсе.
type Assignment struct {
	ID          int64  `json:"id"`
	CourseID    int64  `json:"course_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	DueDate     int64  `json:"due_date"` // Unix timestamp, 0 = no deadline
	CreatedBy   int64  `json:"created_by"`
}

// CreateAssignmentInput — данные для создания задания.
type CreateAssignmentInput struct {
	CourseID    int64
	Title       string
	Description string
	DueDate     int64
}

// Submission — сдача работы студентом по заданию.
type Submission struct {
	ID           int64   `json:"id"`
	AssignmentID int64   `json:"assignment_id"`
	UserID       int64   `json:"user_id"`
	Content      string  `json:"content"`
	Score        float64 `json:"score"` // -1 if ungraded
	Comment      string  `json:"comment"`
	SubmittedAt  int64   `json:"submitted_at"`
	GradedAt     int64   `json:"graded_at"`

	// Join-поля для UI (заполняются только в списочных запросах).
	UserName     string `json:"user_name,omitempty"`
	UserUsername string `json:"user_username,omitempty"`
	AssignTitle  string `json:"assign_title,omitempty"`
}

// SubmitInput — данные для сдачи работы.
type SubmitInput struct {
	AssignmentID int64
	Content      string
}

// GradeInput — данные для оценки сдачи.
type GradeInput struct {
	SubmissionID int64
	Score        float64
	Comment      string
}

// ScheduleFeedItem — элемент агрегирующей ленты расписания.
// В этом этапе лента собирается из дедлайнов заданий (kind всегда "deadline");
// ручные события/пары появятся, когда введём таблицу schedule_items.
type ScheduleFeedItem struct {
	AssignmentID int64   `json:"assignment_id"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	DueDate      int64   `json:"due_date"`
	CourseID     int64   `json:"course_id"`
	CourseTitle  string  `json:"course_title"`
	State        string  `json:"state"` // none | submitted | graded | own
	Score        float64 `json:"score"` // -1 если оценки нет
}
