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
	DueDate     int64  `json:"due_date"`
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
	Score        float64 `json:"score"`
	Comment      string  `json:"comment"`
	SubmittedAt  int64   `json:"submitted_at"`
	GradedAt     int64   `json:"graded_at"`

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

// Resource — полезный материал/ссылка (глобальная витрина).
type Resource struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Category    string `json:"category"`
	URL         string `json:"url"`
	Description string `json:"description"`
	CreatedBy   int64  `json:"created_by"`
	CreatedAt   int64  `json:"created_at"`
}

// CreateResourceInput — данные для создания ресурса.
type CreateResourceInput struct {
	Title       string
	Category    string
	URL         string
	Description string
}

// Event — анонс/событие с датой (глобальный).
//
// PlaceID — ссылка на справочник мест; nil означает «событие ни к какому месту
// не привязано». Указатель, а не 0: 0 — число, которое карта прочла бы как
// «место выбрано, только странное», тогда как «не выбрано» — другое состояние и
// называется оно по-другому (no_place).
//
// Category — рубрика, которую вводит человек; пустая строка значит «без
// категории». Справочника рубрик нет намеренно: выдумывать их означало бы
// показывать вузу список, которого он не составлял.
type Event struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	EventDate   int64  `json:"event_date"`
	PlaceID     *int64 `json:"place_id"`
	Category    string `json:"category"`
	CreatedBy   int64  `json:"created_by"`
	CreatedAt   int64  `json:"created_at"`
}

// CreateEventInput — данные для создания события.
type CreateEventInput struct {
	Title       string
	Description string
	EventDate   int64
	PlaceID     *int64
	Category    string
}

// ScheduleFeedItem — элемент агрегирующей ленты.
// Kind различает источник: "deadline" (задание) или "event" (событие).
type ScheduleFeedItem struct {
	Kind         string  `json:"kind"`
	AssignmentID int64   `json:"assignment_id,omitempty"`
	EventID      int64   `json:"event_id,omitempty"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	DueDate      int64   `json:"due_date"`
	CourseID     int64   `json:"course_id,omitempty"`
	CourseTitle  string  `json:"course_title,omitempty"`
	State        string  `json:"state,omitempty"` // none|submitted|graded|own (только для deadline)
	Score        float64 `json:"score"`           // -1 если оценки нет
}
